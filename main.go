package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/saltsa/apprunner/validation"
	"github.com/spf13/viper"
)

var (
	configReloadInterval   = 15 * time.Second
	cmdHealthCheckInterval = 10 * time.Second
)

var cfg *Config
var runs = make(map[string]*currentRun)

var client = &http.Client{
	Timeout: 30 * time.Second,
}

type Config struct {
	ConfigURL  string `mapstructure:"config_url"`
	GithubUser string `mapstructure:"github_user"`
}

type ConfigResponse struct {
	Apps map[string]*DeployConfig
}
type DeployConfig struct {
	SHA256Sum    string
	Version      string
	Source       string
	Env          []string
	AppName      string
	Signatures   []string
	lastModified time.Time
}

func (dc *DeployConfig) String() string {
	return fmt.Sprintf("app version %s from %s", dc.Version, dc.Source)
}

func (dc *DeployConfig) Verify() error {
	return errors.Join(
		validation.VerifySHA256Sum(dc.SHA256Sum),
		validation.VerifySource(dc.Source, cfg.GithubUser),
		validation.VerifyVersion(dc.Version),
		validation.VerifySignature(dc.SHA256Sum, dc.Signatures),
	)
}

type currentRun struct {
	sync.RWMutex
	Version        string
	Location       string
	Env            []string
	reload         chan struct{}
	runInitialized time.Time
	appName        string
	running        bool
	cmd            *exec.Cmd
	ctx            context.Context
	cancelFunc     context.CancelFunc
}

func (cr *currentRun) String() string {
	return fmt.Sprintf("<%s %s started at %s>", cr.appName, cr.Version, cr.runInitialized.Format("15:04:05"))
}

func (cr *currentRun) SetRunning(dc *DeployConfig) {
	cr.Lock()
	defer cr.Unlock()
	currentVer := cr.Version
	currentInit := cr.runInitialized

	// TODO: Visit this. For debugging purposes it's useful to consider last modified, otherwise only version
	if currentVer != dc.Version || (!dc.lastModified.IsZero() && dc.lastModified.After(currentInit)) {
		log.Printf("deploying a new version: %s", dc.Version)
		location, err := downloadApp(dc)
		if err != nil {
			log.Printf("failure to deploy app: %s", err)
			return
		}

		cr.Version = dc.Version
		cr.Location = location
		cr.Env = dc.Env
		cr.appName = dc.AppName
		cr.runInitialized = time.Now()
		select {
		case cr.reload <- struct{}{}:
		default:
		}
	} else {
		log.Printf("version %s already deployed", dc.Version)
	}
}

func (cr *currentRun) Stop() {

	cr.Lock()
	defer cr.Unlock()

	if cr.cmd == nil || cr.cmd.Process == nil {
		return
	}
	log.Printf("stopping process %s", cr)

	log.Printf("wait cmd to finish...")
	cr.cmd.Process.Signal(syscall.SIGTERM)
	err := cr.cmd.Wait()
	if err != nil {
		log.Printf("wait returned error: %s", err)
	}

	cr.cancelFunc()

	log.Printf("process final state: %s", cr.cmd.ProcessState)

	cr.cmd = nil

	log.Printf("stopped run %s", cr)
}

func NewCurrentRun(appName string) *currentRun {
	run, ok := runs[appName]
	if ok {
		return run
	}
	log.Printf("create new run for app `%s`", appName)

	c := &currentRun{}
	runs[appName] = c
	c.reload = make(chan struct{}, 1)

	return c
}

func CleanRuns() {
	for _, run := range runs {
		run.Stop()
	}
}

func getConfig() *Config {
	viper.AddConfigPath(".")
	viper.SetConfigName("app")
	viper.SetConfigType("env")

	viper.AutomaticEnv()
	err := viper.ReadInConfig()
	if err != nil {
		log.Fatalln(err)
	}

	for _, key := range viper.AllKeys() {
		log.Printf("config key: %s", key)
	}
	cfg := Config{}
	err = viper.Unmarshal(&cfg)
	if err != nil {
		log.Fatalln(err)
	}

	return &cfg
}

func getDeployConfig(cfg *Config) (*ConfigResponse, error) {
	resp, err := client.Get(cfg.ConfigURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid status: %d", resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)

	apps := &ConfigResponse{}

	err = dec.Decode(apps)
	if err != nil {
		return nil, err
	}

	log.Printf("got deploy config: %s", apps)

	lm := resp.Header.Get("last-modified")
	lastModified, err := time.Parse(time.RFC1123, lm)
	if err != nil {
		log.Printf("could not get last modified: %s", err)
	}

	for _, dc := range apps.Apps {
		dc.lastModified = lastModified

		err := dc.Verify()
		if err != nil {
			return nil, err
		}
	}

	return apps, nil
}

func downloadApp(dc *DeployConfig) (string, error) {
	url := dc.Source
	expectHash := dc.SHA256Sum
	log.Printf("downloading the application...")
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 {
		return "", fmt.Errorf("invalid status %d or no content length %d", resp.StatusCode, resp.ContentLength)
	}
	log.Printf("application size: %d", resp.ContentLength)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// verify downloaded matches the sha256sum
	sumArray := sha256.Sum256(body)
	downloadedSum := fmt.Sprintf("%x", sumArray)
	if downloadedSum != expectHash {
		return "", fmt.Errorf("downloaded `%s` but expected `%s`", downloadedSum, expectHash)
	}

	log.Printf("application downloaded and verified successfully")

	// write app to temp dir
	tmpdir := filepath.Join(os.TempDir(), "apprunner")
	err = os.MkdirAll(tmpdir, 0700)
	if err != nil {
		return "", err
	}

	pattern := "app*"
	if runtime.GOOS == "windows" {
		pattern = "app*.exe"
	}
	f, err := os.CreateTemp(tmpdir, pattern)
	if err != nil {
		return "", err
	}

	err = f.Chmod(0700)
	if err != nil {
		return "", err
	}

	_, err = f.Write(body)
	if err != nil {
		return "", err
	}

	err = errors.Join(
		f.Sync(),
		f.Close(),
	)
	if err != nil {
		return "", err
	}

	log.Printf("new version downloaded to: %s", f.Name())

	return f.Name(), nil
}

func main() {
	log.SetFlags(log.Lmicroseconds)
	cfg = getConfig()
	log.Printf("cfg: %+v", cfg)

	err := validation.UpdateValidKeys(cfg.GithubUser)
	if err != nil {
		log.Fatalf("error updating keys from github: %s", err)
	}

	go func() {
		for {
			resp, err := getDeployConfig(cfg)
			if err != nil {
				log.Printf("failure to fetch deploy config: %s", err)
				time.Sleep(configReloadInterval)
				continue
			}

			// CleanRuns()
			for app, dc := range resp.Apps {
				cr := NewCurrentRun(app)
				cr.SetRunning(dc)
				go runApp(cr)
			}
			time.Sleep(configReloadInterval)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	for {
		recv := <-quit
		if recv == os.Interrupt {
			os.Stdout.Write([]byte("\r"))
		}

		log.Printf("got signal: %v", recv)

		if recv == os.Interrupt || recv == syscall.SIGTERM {
			log.Println("quitting")
			return
		}
	}
}
