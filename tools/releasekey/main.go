// Command releasekey manages the Ed25519 key that signs dashboard releases.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = generate(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "pubkey":
		err = pubkey()
	case "check-embedded":
		err = checkEmbedded(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: releasekey generate|sign|pubkey|check-embedded [flags]")
	os.Exit(2)
}

func generate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	out := fs.String("out", "release-signing.key", "file for the private key")
	_ = fs.Parse(args)

	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s already exists, refusing to overwrite it", *out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(*out, []byte(seed+"\n"), 0600); err != nil {
		return err
	}
	fmt.Printf("Private key written to %s\n", *out)
	fmt.Println("  -> Store its content as GitHub secret RELEASE_SIGNING_KEY (environment \"release\"),")
	fmt.Println("     then delete the file or keep it in a password manager. Never commit it.")
	fmt.Println()
	fmt.Println("Public key (put it into releasePublicKey in main.go and RELEASE_PUBLIC_KEY in install-dashboard.sh):")
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

func privateKeyFromEnv() (ed25519.PrivateKey, error) {
	raw := strings.TrimSpace(os.Getenv("RELEASE_SIGNING_KEY"))
	if raw == "" {
		return nil, errors.New("RELEASE_SIGNING_KEY is not set")
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("RELEASE_SIGNING_KEY is not a base64 encoded Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	in := fs.String("in", "SHA256SUMS", "file to sign")
	out := fs.String("out", "SHA256SUMS.sig", "signature output")
	_ = fs.Parse(args)

	priv, err := privateKeyFromEnv()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, data)
	return os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0644)
}

func pubkey() error {
	priv, err := privateKeyFromEnv()
	if err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	return nil
}

// reEmbedded matches releasePublicKey = "..." (main.go) and RELEASE_PUBLIC_KEY="..." (installer).
var reEmbedded = regexp.MustCompile(`(?:releasePublicKey\s*=\s*|RELEASE_PUBLIC_KEY=)"([^"]*)"`)

type fileList []string

func (f *fileList) String() string     { return strings.Join(*f, ",") }
func (f *fileList) Set(v string) error { *f = append(*f, v); return nil }

func checkEmbedded(args []string) error {
	fs := flag.NewFlagSet("check-embedded", flag.ExitOnError)
	var files fileList
	fs.Var(&files, "file", "file containing the embedded public key (repeatable)")
	_ = fs.Parse(args)
	if len(files) == 0 {
		files = fileList{"main.go"}
	}

	priv, err := privateKeyFromEnv()
	if err != nil {
		return err
	}
	want := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		m := reEmbedded.FindSubmatch(src)
		if m == nil || len(m[1]) == 0 {
			return fmt.Errorf("the release public key in %s is empty: dashboards could not verify this release", file)
		}
		if string(m[1]) != want {
			return fmt.Errorf("the release public key in %s does not belong to RELEASE_SIGNING_KEY", file)
		}
		fmt.Printf("%s: embedded release public key matches the signing key.\n", file)
	}
	return nil
}
