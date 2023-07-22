package main

import (
	"encoding/base64"
	"log"
	"net"
	"os"

	"golang.org/x/crypto/ssh/agent"
)

func sshSign(message string) []string {
	socket := os.Getenv("SSH_AUTH_SOCK")
	conn, err := net.Dial("unix", socket)
	if err != nil {
		log.Fatalf("Failed to open SSH_AUTH_SOCK: %v", err)
	}

	defer conn.Close()

	agentClient := agent.NewClient(conn)

	keys, err := agentClient.List()
	if err != nil {
		log.Fatalf("failed to list keys: %v", err)
	}

	var ret []string
	for _, key := range keys {
		// log.Printf("ssh key: %s", key)
		sig, err := agentClient.Sign(key, []byte(message))
		if err != nil {
			log.Fatalf("failed to sign: %v", err)
		}

		sigString := base64.RawURLEncoding.EncodeToString(sig.Blob)
		// log.Printf("signed b64: %s", sigString)
		ret = append(ret, sigString)
	}

	return ret
}
