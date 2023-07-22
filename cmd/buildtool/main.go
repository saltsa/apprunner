package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/saltsa/apprunner"
)

const targetDir = "dist"

var cfg *apprunner.Config

var buildEnvs = []BuildEnv{
	// {"linux", "amd64"},
	{"darwin", "arm64"},
	// {"windows", "386"},
}

type BuildEnv struct {
	Goos   string
	Goarch string
}

func (be BuildEnv) Target(appName string) string {
	base := fmt.Sprintf("%s_%s", appName, be.Goos)
	if be.Goos == "windows" {
		base += ".exe"
	}
	return base
}

func (be BuildEnv) String() string {
	return fmt.Sprintf("%s/%s", be.Goos, be.Goarch)
}

func build(appName, version string) ([]apprunner.DeployConfig, error) {

	err := os.MkdirAll(targetDir, 0755)
	if err != nil {
		return nil, err
	}

	c := exec.Command("go", "version")
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	err = c.Run()
	if err != nil {
		return nil, err
	}

	var dcs []apprunner.DeployConfig
	for _, e := range buildEnvs {
		targetFile := filepath.Join(targetDir, e.Target(appName))
		log.Printf("build for: %s, resulting binary in %s", e, targetFile)
		c := exec.Command("go", "build", "-trimpath", "-o", targetFile)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = []string{
			"HOME=" + os.Getenv("HOME"),
			"USER=" + os.Getenv("USER"),
			"PWD=" + os.Getenv("PWD"),
			"CGO_ENABLED=0",
			"GOOS=" + e.Goos,
			"GOARCH=" + e.Goarch,
		}
		err = c.Run()
		if err != nil {
			return nil, err
		}

		body, err := os.ReadFile(targetFile)
		if err != nil {
			return nil, err
		}

		sum := sha256.Sum256(body)

		dc := buildDeployConfig(cfg, appName, version, fmt.Sprintf("%x", sum), e)
		dcs = append(dcs, dc)
	}

	return dcs, nil
}

func buildDeployConfig(cfg *apprunner.Config, appName string, version string, sum string, be BuildEnv) apprunner.DeployConfig {
	dc := apprunner.DeployConfig{
		Goarch:    be.Goarch,
		Goos:      be.Goos,
		AppName:   appName,
		SHA256Sum: sum,
		Version:   version,
		Source: fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
			cfg.GithubUser,
			appName,
			version,
			be.Target(appName),
		),
		Signatures: sshSign(sum),
	}

	log.Printf("verify")
	err := dc.Verify()
	if err != nil {
		log.Printf("verify failure: %+v", err)
	}

	b, err := json.Marshal(dc)
	if err != nil {
		log.Fatalln(err)
	}

	log.Printf("deploy config: %s", b)

	return dc
}

func usage() {
	fmt.Printf("usage: %s <appname> <version>\n", os.Args[0])
	os.Exit(1)
}

func main() {
	flag.Parse()

	cfg = apprunner.GetConfig()
	appName := flag.Arg(0)
	version := flag.Arg(1)

	if appName == "" {
		usage()
	}

	if version == "" {
		usage()
	}

	deployConfigs, err := build(appName, version)
	if err != nil {
		log.Printf("build failure: %s", err)
		os.Exit(1)
	}

	err = uploadDeployments(deployConfigs)
	if err != nil {
		log.Printf("deployment update failure: %s", err)
		os.Exit(1)
	}
	log.Printf("everything built")
}
