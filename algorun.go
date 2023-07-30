package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/cavaliergopher/grab/v3"
	"github.com/docopt/docopt-go"
	"github.com/schollz/progressbar/v3"

	"github.com/google/go-github/v53/github"
)

var Props struct {
	AlgoRunDir   string
	DownloadsDir string
	ArchivesDir  string
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
		log.Fatalln(err)
	}

	for i := 0; i < len(releases); i++ {
		tag := *releases[i].TagName
		if strings.Contains(tag, match) {
			return tag, nil
		}
	}

	return "", errors.New("no match found")
}

func createCmd(config Config, release string) {
	versionString, err := getVersion(release)
	if err != nil {
		log.Fatalln(err)
	}

	versionRegex := regexp.MustCompile(`\d+\.\d+\.\d+`)
	channelRegex := regexp.MustCompile(`-(.*)`)

	version := versionRegex.FindString(versionString)

	algorunTarballName := fmt.Sprintf("algorun_%s-%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH, versionString)

	algorunTarball := filepath.Join(Props.DownloadsDir, algorunTarballName)

	if _, err := os.Stat(algorunTarball); err == nil {
		fmt.Printf("File exists\n")
	} else {
		releaseChannel := channelRegex.FindString(versionString)[1:]

		releaseTarballName := fmt.Sprintf("node_%s_%s-%s_%s.tar.gz", releaseChannel, runtime.GOOS, runtime.GOARCH, version)
		awsUrl := fmt.Sprintf("https://algorand-releases.s3.amazonaws.com/channel/%s/%s", releaseChannel, releaseTarballName)

		downloadFile(awsUrl, Props.DownloadsDir, "Downloading release tarball")
	}
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
		log.Fatalln(err)
		return
	}

	var config Config

	err = opts.Bind(&config)
	if err != nil {
		log.Fatalln(err)
		return
	}

	release := config.Release

	if release == "" {
		release = "stable"
	}

	Props.AlgoRunDir, err = filepath.Abs(filepath.Join(".", "test-algorun-dir"))

	if err != nil {
		log.Fatalln(err)
	}

	Props.DownloadsDir = filepath.Join(Props.AlgoRunDir, "downloads")
	Props.ArchivesDir = filepath.Join(Props.AlgoRunDir, "archives")

	if config.Create {
		createCmd(config, release)
	} else {
		log.Fatalln("Unrecognized command")
	}
}
