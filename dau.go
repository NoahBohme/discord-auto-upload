package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pborman/getopt"
)

const currentVersion = "0.5"

var lastCheck = time.Now()
var newLastCheck = time.Now()

// Config for the application
type Config struct {
	webhookURL string
	path       string
	watch      int
	username   string
}

func main() {

	config := parseOptions()

	checkPath(config.path)
	checkUpdates()

	log.Print("Waiting for images to appear in ", config.path)
	// wander the path, forever
	for {
		err := filepath.Walk(config.path,
			func(path string, f os.FileInfo, err error) error { return checkFile(path, f, err, config) })
		if err != nil {
			log.Fatal("could not watch path", err)
		}
		lastCheck = newLastCheck
		time.Sleep(time.Duration(config.watch) * time.Second)
	}
}

func checkPath(path string) {
	src, err := os.Stat(path)
	if err != nil {
		log.Fatal(err)
	}
	if !src.IsDir() {
		log.Fatal(path, " is not a directory")
		os.Exit(1)
	}
}

func checkUpdates() {

	type GithubRelease struct {
		HTMLURL string
		TagName string
		Name    string
		Body    string
	}

	client := &http.Client{Timeout: time.Second * 5}
	resp, err := client.Get("https://api.github.com/repos/tardisx/discord-auto-upload/releases/latest")
	if err != nil {
		log.Fatal("could not check for updates:", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal("could not check read update response")
	}

	var latest GithubRelease
	err = json.Unmarshal(body, &latest)

	if err != nil {
		log.Fatal("could not parse JSON: ", err)
	}

	if currentVersion < latest.TagName {
		fmt.Printf("You are currently on version %s, but version %s is available\n", currentVersion, latest.TagName)
		fmt.Println("----------- Release Info -----------")
		fmt.Println(latest.Body)
		fmt.Println("------------------------------------")
	}

}

func parseOptions() Config {

	var newConfig Config
	// Declare the flags to be used
	webhookFlag := getopt.StringLong("webhook", 'w', "", "discord webhook URL")
	pathFlag := getopt.StringLong("directory", 'd', "", "directory to scan, optional, defaults to current directory")
	watchFlag := getopt.Int16Long("watch", 's', 10, "time between scans")
	usernameFlag := getopt.StringLong("username", 'u', "", "username for the bot upload")
	helpFlag := getopt.BoolLong("help", 'h', "help")
	versionFlag := getopt.BoolLong("version", 'v', "show version")
	getopt.SetParameters("")

	getopt.Parse()

	if *helpFlag {
		getopt.PrintUsage(os.Stderr)
		os.Exit(0)
	}

	if *versionFlag {
		fmt.Println("dau - https://github.com/tardisx/discord-auto-upload")
		fmt.Printf("Version: %s\n", currentVersion)
		os.Exit(0)
	}

	if !getopt.IsSet("directory") {
		*pathFlag = "./"
		log.Println("Defaulting to current directory")
	}

	if !getopt.IsSet("webhook") {
		log.Fatal("ERROR: You must specify a --webhook URL")
	}

	newConfig.path = *pathFlag
	newConfig.webhookURL = *webhookFlag
	newConfig.watch = int(*watchFlag)
	newConfig.username = *usernameFlag

	return newConfig
}

func checkFile(path string, f os.FileInfo, err error, config Config) error {

	if f.ModTime().After(lastCheck) && f.Mode().IsRegular() {

		if fileEligible(config, path) {
			// process file
			processFile(config, path)
		}

		if newLastCheck.Before(f.ModTime()) {
			newLastCheck = f.ModTime()
		}
	}

	return nil
}

func fileEligible(config Config, file string) bool {
	extension := strings.ToLower(filepath.Ext(file))
	if extension == ".png" || extension == ".jpg" || extension == ".gif" {
		return true
	}
	return false
}

func processFile(config Config, file string) {
	log.Print("Uploading ", file)

	extraParams := map[string]string{}

	if config.username != "" {
		extraParams["username"] = config.username
	}

	type DiscordAPIResponseAttachment struct {
		URL      string
		ProxyURL string
		Size     int
		Width    int
		Height   int
		Filename string
	}

	type DiscordAPIResponse struct {
		Attachments []DiscordAPIResponseAttachment
		ID          int64 `json:",string"`
	}

	request, err := newfileUploadRequest(config.webhookURL, extraParams, "file", file)
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	client := &http.Client{Timeout: time.Second * 30}
	resp, err := client.Do(request)
	if err != nil {

		log.Fatal("Error performing request:", err)

	} else {

		if resp.StatusCode != 200 {
			log.Print("Bad response from server:", resp.StatusCode)
			return
		}

		resBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Fatal("could not deal with body", err)
		}
		resp.Body.Close()

		var res DiscordAPIResponse
		err = json.Unmarshal(resBody, &res)

		if err != nil {
			log.Print("could not parse JSON: ", err)
			fmt.Println("Response was:", string(resBody[:]))
			return
		}
		if len(res.Attachments) < 1 {
			log.Print("bad response - no attachments?")
			return
		}
		var a = res.Attachments[0]
		elapsed := time.Since(start)
		rate := float64(a.Size) / elapsed.Seconds() / 1024.0

		log.Printf("Uploaded to %s %dx%d", a.URL, a.Width, a.Height)
		log.Printf("id: %d, %d bytes transferred in %.2f seconds (%.2f KiB/s)", res.ID, a.Size, elapsed.Seconds(), rate)
	}

}

func newfileUploadRequest(uri string, params map[string]string, paramName, path string) (*http.Request, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(paramName, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(part, file)
	if err != nil {
		log.Fatal("Could not copy: ", err)
	}

	for key, val := range params {
		_ = writer.WriteField(key, val)
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", uri, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, err
}
