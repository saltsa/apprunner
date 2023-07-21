package validation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

func VerifySHA256Sum(sum string) error {
	if len(sum) != sha256.Size*2 {
		return errors.New("invalid sum len")
	}
	return nil
}

func VerifySource(source string, githubUser string) error {
	prefix := fmt.Sprintf("https://github.com/%s/", githubUser)
	if !strings.HasPrefix(source, prefix) {
		return fmt.Errorf("invalid source, must start with `%s`", prefix)
	}
	return nil
}

func VerifyVersion(ver string) error {
	if !semver.IsValid(ver) {
		return errors.New("version must be semver")
	}
	return nil
}
