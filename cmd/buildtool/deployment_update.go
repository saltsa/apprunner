package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/saltsa/apprunner"
)

const signatureHeader = "x-signature"

func uploadDeployments(dcs []apprunner.DeployConfig) error {

	// data := make(map[string]apprunner.DeployConfig)

	data := make(map[string]*apprunner.DeployConfig)
	payloadStruct := apprunner.ConfigResponse{
		Apps: data,
	}

	for _, dc := range dcs {
		data[dc.AppName] = &dc
	}

	payload, err := json.Marshal(payloadStruct)
	if err != nil {
		return err
	}
	log.Printf("data to be sent: %s", payload)

	reader := bytes.NewReader(payload)
	req, err := http.NewRequest("POST", cfg.ConfigURL, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	payloadHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	payloadSignature := sshSign(payloadHash)[0]
	req.Header.Set(signatureHeader, payloadSignature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	log.Printf("http response: %d", resp.StatusCode)
	log.Printf("http body: %s", body)

	if resp.StatusCode == http.StatusOK {
		log.Printf("deployment successfully updated")
		log.Printf("see `curl %s` to check current config", cfg.ConfigURL)
	}
	return nil
}
