// Package salt implements the rotate-salt and salt CLI commands.
package salt

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
)

// RotateSalt generates a new integrity salt and re-signs every existing
// change row so the HMAC chain remains valid under the new key. The existing
// chain is verified first unless -force is passed.
func RotateSalt(args []string) error {
	fs := flag.NewFlagSet("rotate-salt", flag.ExitOnError)
	force := fs.Bool("force", false, "rotate even if the existing chain has integrity problems")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if err := integrity.RotateSalt(root, *force); err != nil {
		return err
	}
	fmt.Println("salt rotated; all rows re-signed with the new key")
	return nil
}

// Salt locates, exports, and imports the integrity salt. The salt lives
// outside the repository on purpose, which means moving to a new machine or
// recovering a backup needs a way to carry it across.
func Salt(args []string) error {
	const usage = "usage: githints salt path | export [-o FILE] | import [-force] FILE"
	if len(args) == 0 {
		return errors.New(usage)
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	switch args[0] {
	case "path":
		fmt.Println(integrity.SaltPath(root))
		return nil

	case "export":
		fs := flag.NewFlagSet("salt export", flag.ExitOnError)
		out := fs.String("o", "-", "file to write (created 0600; must not exist), or - for stdout")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		encoded, err := integrity.ExportSalt(root)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "githints: this is the integrity key for this repository's change log; keep it as private as a password")
		if *out == "-" {
			fmt.Println(encoded)
			return nil
		}
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(f, encoded); err != nil {
			_ = f.Close() // the write error is the one worth reporting
			return err
		}
		return f.Close()

	case "import":
		fs := flag.NewFlagSet("salt import", flag.ExitOnError)
		force := fs.Bool("force", false, "replace an existing salt (orphans every row signed with it)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New(usage)
		}
		data, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		path, err := integrity.ImportSalt(root, string(data), *force)
		if err != nil {
			return err
		}
		fmt.Printf("salt installed at %s; run `githints verify` to confirm it matches the log\n", path)
		return nil
	}
	return errors.New(usage)
}
