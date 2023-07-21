package main

import (
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
	)
}

type currentRun struct {
	sync.RWMutex
	Version        string
	Location       string
	Env            []string
	reload         chan struct{}
	cmd            *exec.Cmd
	runInitialized time.Time
	appName        string
}

func (cr *currentRun) SetRunning(dc *DeployConfig) {
	cr.Lock()
	defer cr.Unlock()

	// TODO: Visit this. For debugging purposes it's useful to consider last modified, otherwise only version
	if cr.Version != dc.Version || (!dc.lastModified.IsZero() && dc.lastModified.After(cr.runInitialized)) {
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
	log.Printf("stopping run %#v", cr)

	if cr.cmd != nil {
		if cr.cmd.Process != nil {
			err := cr.cmd.Process.Kill()
			if err != nil {
				log.Printf("kill returned error: %s", err)
			}
		}
		err := cr.cmd.Wait()
		if err != nil {
			log.Printf("wait returned error: %s", err)
		}
	}
}

func NewCurrentRun(appName string, dc *DeployConfig) *currentRun {
	log.Printf("start app `%s`", appName)
	run, ok := runs[appName]
	if ok {
		return run
	}
	c := &currentRun{}
	runs[appName] = c

	c.reload = make(chan struct{}, 1)
	c.SetRunning(dc)

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

	sumArray := sha256.Sum256(body)
	downloadedSum := fmt.Sprintf("%x", sumArray)
	if downloadedSum != expectHash {
		return "", fmt.Errorf("downloaded `%s` but expected `%s`", downloadedSum, expectHash)
	}

	log.Printf("application downloaded and verified successfully")

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

	log.Printf("new version downloaded to: %s", f.Name())

	return f.Name(), nil
}

func main() {
	log.SetFlags(log.Lmicroseconds)
	cfg = getConfig()
	log.Printf("cfg: %+v", cfg)

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
				log.Printf("get config run for app %s", app)
				cr := NewCurrentRun(app, dc)
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
