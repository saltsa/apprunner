package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	ConfigURL string `mapstructure:"config_url"`
}

type DeployConfig struct {
	SHA256Sum string
	Version   string
	Source    string
}

type currentRun struct {
	sync.Mutex
	Version  string
	Location string
	running  bool
}

func (cr *currentRun) SetRunning(dc *DeployConfig) {
	cr.Lock()
	defer cr.Unlock()

	if cr.Version != dc.Version {
		log.Printf("we have a new version: %s", dc.Version)
		location, err := downloadApp(dc)
		if err != nil {
			log.Printf("failure to deploy app: %s", err)
			return
		}
		cr.Version = dc.Version
		cr.Location = location
	} else {
		log.Printf("version %s already deployed", dc.Version)
	}
}

var client = &http.Client{
	Timeout: 30 * time.Second,
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

func getDeployConfig(cfg *Config) (*DeployConfig, error) {
	resp, err := client.Get(cfg.ConfigURL)
	if err != nil {
		log.Printf("failure to get config: %s", err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid status: %d", resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)

	dc := DeployConfig{}
	err = dec.Decode(&dc)
	if err != nil {
		log.Printf("failure to get config: %s", err)
		return nil, err
	}

	log.Printf("deploy config: %+v", dc)
	return &dc, nil
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
		return "", fmt.Errorf("invalid status %d or no content length", resp.StatusCode)
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

	f, err := os.CreateTemp(filepath.Join(os.TempDir(), "apprunner"), "app")
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
	cfg := getConfig()
	log.Printf("cfg: %+v", cfg)

	cr := &currentRun{}

	for {
		dc, err := getDeployConfig(cfg)
		if err != nil {
			log.Printf("failure to fetch deploy config: %s", err)
		}
		cr.SetRunning(dc)
		time.Sleep(60 * time.Second)
	}
}
