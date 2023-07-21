package validation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"golang.org/x/mod/semver"
)

const SupportedSSHKey = "ssh-ed25519"

// Keydata should be this long: 4 B len + 11 B string "ssh-ed25519" + 4 B len + 32 B key
const KeyDataLength = 51

func VerifySHA256Sum(sum string) error {
	if len(sum) != sha256.Size*2 {
		return errors.New("invalid sum len")
	}
	return nil
}

func VerifySource(source string, githubUser string) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return err
	}

	if !strings.HasPrefix(parsed.EscapedPath(), "/"+githubUser+"/") {
		return fmt.Errorf("invalid source, must start with `%s`", "/"+githubUser+"/")
	}
	if parsed.Hostname() != "github.com" {
		return errors.New("hostname in source must be github.com")
	}
	return nil
}

func VerifyVersion(ver string) error {
	if !semver.IsValid(ver) {
		return errors.New("version must be semver")
	}
	return nil
}

// VerifySignature verifies signature which should be list of raw url encoded base64 strings
// The messageStr is sha256sum hash in hex-ascii formmat.
func VerifySignature(messageStr string, signatureStrList []string) error {
	var ok bool

	err := VerifySHA256Sum(messageStr)
	if err != nil {
		return err
	}

	message := []byte(messageStr)

	mutex.Lock()
	defer mutex.Unlock()

	for _, signatureStr := range signatureStrList {
		signature, err := base64.RawURLEncoding.DecodeString(signatureStr)
		if err != nil {
			return err
		}
		for _, key := range validKeys {
			ok = ed25519.Verify(key, message, signature)
			if ok {
				return nil
			}
		}
	}
	return errors.New("signature verification failed")
}

var validKeys []ed25519.PublicKey
var mutex sync.Mutex

func UpdateValidKeys(githubUser string) error {
	r, err := http.Get(fmt.Sprintf("https://api.github.com/users/%s/keys", githubUser))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}

	keys := gjson.ParseBytes(body).Get("#.key").Array()
	for _, r := range keys {
		log.Printf("found key: %s", r)
		parts := strings.Split(r.String(), " ")
		if len(parts) == 2 && parts[0] == SupportedSSHKey {
			keyData, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				return err
			}

			if len(keyData) != KeyDataLength {
				return fmt.Errorf("expect %d long key, got %d", KeyDataLength, len(keyData))
			}

			if !bytes.HasPrefix(keyData, append([]byte{0x0, 0x0, 0x0, 0xb}, "ssh-ed25519"...)) {
				return fmt.Errorf("invalid key, prefix: %v", keyData[:19])
			}

			key := ed25519.PublicKey(keyData[19:KeyDataLength])
			mutex.Lock()
			validKeys = append(validKeys, key)
			mutex.Unlock()
		}
	}

	return nil
}
