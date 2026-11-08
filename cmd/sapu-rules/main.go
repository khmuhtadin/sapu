// Command sapu-rules is the owner's tool for signed rule updates: it makes
// a signing key, builds a bundle from a rules folder, signs it and checks
// it. Users never need it; they run `sapu rules install`. See
// docs/rules-updates.md.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	"github.com/khmuhtadin/sapu/internal/safefile"
)

const usage = `Usage:
  sapu-rules keygen KEYFILE
      Write a new private key to KEYFILE (mode 0600; an existing file is
      never overwritten) and print its public key for PublicKeys in
      internal/rulesupdate/sign.go.
  sapu-rules bundle -version V -min-sapu M [-o FILE] DIR
      Build a bundle from the *.yml files in DIR/cleaners, DIR/protect and
      DIR/purge, checked as sapu rules install checks it. It goes to FILE,
      or to stdout.
  sapu-rules sign BUNDLE KEYFILE
      Check BUNDLE and write its signature to BUNDLE.sig.
  sapu-rules verify [-sig SIG] BUNDLE PUBKEY
      Check BUNDLE against the base64 public key PUBKEY with its signature
      (BUNDLE.sig unless -sig is given), and check its rule files.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// usageError is a mistake in the command line: exit code 2.
type usageError struct{ error }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = keygen(args[1:], stdout)
	case "bundle":
		err = bundle(args[1:], stdout, stderr)
	case "sign":
		err = sign(args[1:], stdout)
	case "verify":
		err = verify(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		err = usageError{fmt.Errorf("unknown command %q", args[0])}
	}
	var ue usageError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "sapu-rules: %v\n\n%s", err, usage)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "sapu-rules:", err)
		return 1
	}
	return 0
}

func keygen(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return usageError{errors.New("keygen takes one KEYFILE")}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err // an existing key is never replaced
	}
	if _, err := f.WriteString(rulesupdate.EncodeKey(priv) + "\n"); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Private key written to %s. Keep it secret and backed up; never commit it.\n", args[0])
	fmt.Fprintln(stdout, "Public key, for PublicKeys in internal/rulesupdate/sign.go:")
	fmt.Fprintln(stdout, rulesupdate.EncodeKey(pub))
	return nil
}

func bundle(args []string, stdout, stderr io.Writer) error {
	fl := flag.NewFlagSet("bundle", flag.ContinueOnError)
	fl.SetOutput(stderr)
	version := fl.String("version", "", "bundle version, dotted numbers newer than the last one (e.g. 2026.11.20)")
	minSapu := fl.String("min-sapu", "", "oldest Sapu version that may use these rules (e.g. 1.2.0)")
	out := fl.String("o", "", "write the bundle to this file instead of stdout")
	if err := fl.Parse(args); err != nil {
		return usageError{err}
	}
	if fl.NArg() != 1 || *version == "" || *minSapu == "" {
		return usageError{errors.New("bundle needs -version, -min-sapu and one DIR")}
	}
	data, err := rulesupdate.Build(os.DirFS(fl.Arg(0)), *version, *minSapu, time.Now())
	if err != nil {
		return err
	}
	if *out == "" {
		_, err := stdout.Write(data)
		return err
	}
	return writeFile(*out, data)
}

func sign(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return usageError{errors.New("sign takes BUNDLE and KEYFILE")}
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	if _, err := rulesupdate.Check(data); err != nil {
		return fmt.Errorf("not signed: %w", err)
	}
	keyData, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	key, err := rulesupdate.ParsePrivateKey(keyData)
	if err != nil {
		return err
	}
	if err := writeFile(args[0]+".sig", rulesupdate.Sign(key, data)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Signature written to %s.sig\n", args[0])
	return nil
}

func verify(args []string, stdout, stderr io.Writer) error {
	fl := flag.NewFlagSet("verify", flag.ContinueOnError)
	fl.SetOutput(stderr)
	sigFile := fl.String("sig", "", "the signature file (default BUNDLE.sig)")
	if err := fl.Parse(args); err != nil {
		return usageError{err}
	}
	if fl.NArg() != 2 {
		return usageError{errors.New("verify takes BUNDLE and PUBKEY")}
	}
	if *sigFile == "" {
		*sigFile = fl.Arg(0) + ".sig"
	}
	pub, err := rulesupdate.ParsePublicKey(fl.Arg(1))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fl.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sigFile)
	if err != nil {
		return err
	}
	if err := rulesupdate.VerifyKey(pub, data, sig); err != nil {
		return err
	}
	b, err := rulesupdate.Check(data)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "OK: rules %s, made %s, for Sapu %s or newer. The signature and every rule file check out.\n",
		b.Version, b.Created.Format(time.RFC3339), b.MinSapu)
	return nil
}

// writeFile writes data to path, replacing a plain file that is there, never
// through a symlink or a hard link. The root is path's own folder: the owner
// named the file.
func writeFile(path string, data []byte) error {
	return safefile.Write(filepath.Dir(path), path, data, 0o600)
}
