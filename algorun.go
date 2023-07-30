package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/algorand/go-algorand-sdk/client/v2/algod"
	"github.com/cavaliergopher/grab/v3"
	"github.com/docopt/docopt-go"
	"github.com/schollz/progressbar/v3"

	"github.com/codeclysm/extract/v3"
	"github.com/google/go-github/v53/github"

	"github.com/otiai10/copy"
)

var Props struct {
	AlgoRunDir   string
	DownloadsDir string
	BaseDir      string
	TempDir      string
	DataDir      string
	BinDir       string
	GoalPath     string
}

type Config struct {
	Commnad       string   `docopt:"<command>"`
	Create        bool     `docopt:"create"`
	Update        bool     `docopt:"update"`
	Catchup       bool     `docopt:"catchup"`
	Start         bool     `docopt:"start"`
	Stop          bool     `docopt:"stop"`
	Status        bool     `docopt:"status"`
	Goal          bool     `docopt:"goal"`
	Dashboard     bool     `docopt:"dashboard"`
	BaseDir       string   `docopt:"--base-dir"`
	ForceDownload bool     `docopt:"--force-download"`
	Release       string   `docopt:"<release>"`
	GoalArgs      []string `docopt:"<goal-args>"`
}

func bytesToFile(path string, data []byte) {
	err := ioutil.WriteFile(path, data, 0644)
	if err != nil {
		log.Fatal(err)
	}
}

func fileToString(path string) string {
	fileContent, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}

	// Convert []byte to string
	return string(fileContent)
}

func execCmd(command string) error {
	splitCommand := strings.Split(command, " ")
	cmd := exec.Command(splitCommand[0], splitCommand[1:]...)

	stdout, err := cmd.StdoutPipe()

	if err != nil {
		return err
	}

	cmd.Stderr = cmd.Stdout
	err = cmd.Start()

	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)
	}
	err = cmd.Wait()

	if err != nil {
		return err
	}

	return nil
}

func catchup() {
	response, err := http.Get("https://algorand-catchpoints.s3.us-east-2.amazonaws.com/channel/mainnet/latest.catchpoint")
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()

	responseData, err := ioutil.ReadAll(response.Body)
	if err != nil {
		log.Fatal(err)
	}

	goalCmd(fmt.Sprintf("node catchup %s", string(responseData)))
}

func goalCmd(args string) {
	execCmd(fmt.Sprintf("%s -d %s %s", Props.GoalPath, Props.DataDir, args))
}

func nodeStart() {
	goalCmd("node start")
	goalCmd("kmd start -t 0")
}

func nodeStop() {
	goalCmd("node stop")
	goalCmd("kmd stop")
}

func nodeStatus() {
	goalCmd("node status")
}

func downloadFile(url string, dir string, desc string) error {
	grabClient := grab.NewClient()
	req, _ := grab.NewRequest(dir, url)
	resp := grabClient.Do(req)

	bar := progressbar.DefaultBytes(resp.HTTPResponse.ContentLength, desc)

	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()

Loop:
	for {
		select {
		case <-t.C:
			bar.Set64(resp.BytesComplete())

		case <-resp.Done:
			// download is complete
			bar.Set64(resp.BytesComplete())
			break Loop
		}
	}

	return resp.Err()
}

func getVersion(match string) (string, error) {
	client := github.NewClient(nil)

	releases, _, err := client.Repositories.ListReleases(
		context.Background(),
		"algorand",
		"go-algorand",
		&github.ListOptions{},
	)

	if err != nil {
		panic(err)
	}

	for i := 0; i < len(releases); i++ {
		tag := *releases[i].TagName
		if strings.Contains(tag, match) {
			return tag, nil
		}
	}

	return "", errors.New("no match found")
}

func downloadAndExtractRelease(release string) {
	versionString, err := getVersion(release)
	if err != nil {
		panic(err)
	}

	versionRegex := regexp.MustCompile(`\d+\.\d+\.\d+`)
	channelRegex := regexp.MustCompile(`-(.*)`)

	version := versionRegex.FindString(versionString)

	releaseChannel := channelRegex.FindString(versionString)[1:]

	releaseTarballName := fmt.Sprintf("node_%s_%s-%s_%s.tar.gz", releaseChannel, runtime.GOOS, runtime.GOARCH, version)
	awsUrl := fmt.Sprintf("https://algorand-releases.s3.amazonaws.com/channel/%s/%s", releaseChannel, releaseTarballName)

	downloadFile(awsUrl, Props.DownloadsDir, "Downloading release tarball")

	file, _ := os.Open(filepath.Join(Props.DownloadsDir, releaseTarballName))
	extract.Gz(context.Background(), file, filepath.Join(Props.TempDir), nil)
}

func copyBinariesFromTmp() {
	bins := [...]string{"goal", "kmd", "algod"}

	tmp_bin_dir := filepath.Join(Props.TempDir, "bin")

	for _, bin := range bins {
		copy.Copy(filepath.Join(tmp_bin_dir, bin), filepath.Join(Props.BinDir, bin))
	}
}

func createCmd(config Config, release string) {
	nodeStop()
	os.RemoveAll(Props.DataDir)

	copyBinariesFromTmp()

	// TODO: Embed genesis
	mainnetGenesis := filepath.Join(Props.TempDir, "genesis", "mainnet", "genesis.json")
	copy.Copy(mainnetGenesis, filepath.Join(Props.DataDir, "genesis.json"))

	exampleConfig := filepath.Join(Props.TempDir, "data", "config.json.example")
	copy.Copy(exampleConfig, filepath.Join(Props.DataDir, "config.json"))

	nodeStart()
	kmdDirs, err := filepath.Glob(filepath.Join(Props.DataDir, "kmd-*"))

	if err != nil {
		panic(err)
	}

	kmdNet := fileToString(filepath.Join(kmdDirs[0], "kmd.net"))
	algodNet := fileToString(filepath.Join(Props.DataDir, "algod.net"))

	kmdNetSplit := strings.Split(kmdNet, ":")
	kmdPort := strings.TrimSpace(kmdNetSplit[len(kmdNetSplit)-1])

	algodNetSplit := strings.Split(algodNet, ":")
	algodPort := strings.TrimSpace(algodNetSplit[len(algodNetSplit)-1])

	algodConfigPath := filepath.Join(Props.DataDir, "config.json")
	kmdExampleConfigPath := filepath.Join(kmdDirs[0], "kmd_config.json.example")

	var algodConfig map[string]interface{}
	var kmdConfig map[string]interface{}

	algodJson := fileToString(algodConfigPath)
	kmdJson := fileToString(kmdExampleConfigPath)

	json.Unmarshal([]byte(algodJson), &algodConfig)
	json.Unmarshal([]byte(kmdJson), &kmdConfig)

	kmdConfig["allowed_origins"] = []string{"*"}
	kmdConfig["address"] = fmt.Sprintf("0.0.0.0:%s", kmdPort)

	algodConfig["EndpointAddress"] = fmt.Sprintf("0.0.0.0:%s", algodPort)

	updatedAlgodJson, err := json.MarshalIndent(algodConfig, "", "  ")

	if err != nil {
		panic(err)
	}

	updatedKmdJson, err := json.MarshalIndent(kmdConfig, "", "  ")

	if err != nil {
		panic(err)
	}

	bytesToFile(algodConfigPath, updatedAlgodJson)
	bytesToFile(filepath.Join(kmdDirs[0], "kmd_config.json"), updatedKmdJson)

	algodToken := fileToString(filepath.Join(Props.DataDir, "algod.token"))

	algodClient, err := algod.MakeClient(fmt.Sprintf("http://localhost:%s", algodPort), algodToken)

	if err != nil {
		panic(err)
	}

	statusReq := algodClient.Status()

	timeout := time.After(10 * time.Second)
	tick := time.Tick(500 * time.Millisecond)
	firstStatus, err := statusReq.Do(context.Background())

	if err != nil {
		panic(err)
	}

	startingRound := firstStatus.LastRound

Loop:
	for {
		select {
		case <-timeout:
			panic("timed out waiting for node to start syncin")
		case <-tick:
			status, err := statusReq.Do(context.Background())
			if err != nil {
				panic(err)
			}

			if status.LastRound > startingRound {
				break Loop
			}
		}
	}

	catchup()
	fmt.Println("Node is now catching up to mainnet!")
}

func main() {
	usage := `algorun

Usage:
  algorun create [--base-dir=<base-dir>] [--force-download] [<release>]
  algorun update [--force-download] [<release>]
  algorun catchup
  algorun start
  algorun stop
  algorun status
  algorun goal [<goal-args>...]
  algorun dashboard

Options:
  -h --help     Show this screen.
  --version     Show version.`

	opts, err := docopt.ParseArgs(usage, nil, "0.1.0")
	if err != nil {
		panic(err)
	}

	var config Config

	err = opts.Bind(&config)
	if err != nil {
		panic(err)
	}

	release := config.Release

	if release == "" {
		release = "stable"
	}

	Props.AlgoRunDir, err = filepath.Abs(filepath.Join(".", "test-algorun-dir"))

	if err != nil {
		panic(err)
	}

	Props.DownloadsDir = filepath.Join(Props.AlgoRunDir, "downloads")
	Props.TempDir = filepath.Join(Props.AlgoRunDir, "temp")
	Props.BaseDir = filepath.Join(Props.AlgoRunDir, "base")
	Props.DataDir = filepath.Join(Props.BaseDir, "data")
	Props.BinDir = filepath.Join(Props.BaseDir, "bin")
	Props.GoalPath = filepath.Join(Props.BinDir, "goal")

	for _, dir := range [...]string{
		Props.DownloadsDir,
		Props.TempDir,
		Props.DataDir,
		Props.BinDir,
	} {
		os.MkdirAll(dir, 0755)
	}

	if config.Create || config.Update {
		downloadAndExtractRelease(release)
	}

	if config.Create {
		createCmd(config, release)
	} else if config.Update {
		nodeStop()
		copyBinariesFromTmp()
		nodeStart()
	} else if config.Status {
		nodeStatus()
	} else {
		panic("Unrecognized command")
	}
}
