package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yxxbc/wmlUpdate/internal/api"
	"github.com/yxxbc/wmlUpdate/internal/engine"
	"github.com/yxxbc/wmlUpdate/internal/manifest"
	"github.com/yxxbc/wmlUpdate/internal/publish"
	"github.com/yxxbc/wmlUpdate/internal/sign"
)

func (a *app) serveCmd() *cobra.Command {
	var (
		listen      string
		token       string
		tokenEnv    string
		tokenFile   string
		allowRemote bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local HTTP integration API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			resolvedToken, generated, source, err := resolveToken(token, tokenEnv, tokenFile)
			if err != nil {
				return usageError(err.Error())
			}
			host, _, err := net.SplitHostPort(listen)
			if err != nil {
				return usageError(fmt.Sprintf("invalid --listen %q: %v", listen, err))
			}
			if !allowRemote && !isLoopback(host) {
				return usageError("refusing to bind a non-loopback address without --allow-remote")
			}
			l, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen: %w", err)
			}
			srv, err := api.New(cfg, p, api.Options{Token: resolvedToken})
			if err != nil {
				_ = l.Close()
				return err
			}
			url := "http://" + l.Addr().String()
			ready := map[string]any{
				"type":         "ready",
				"url":          url,
				"address":      l.Addr().String(),
				"pid":          os.Getpid(),
				"token_source": source,
			}
			if generated {
				ready["token"] = resolvedToken
			}
			if allowRemote {
				fmt.Fprintln(os.Stderr, "warning: API is reachable beyond loopback; terminate TLS and authentication at a reverse proxy")
			}
			if a.jsonOut {
				_ = a.emitObject(ready)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Listening on %s\n", url)
				if generated {
					fmt.Fprintf(cmd.OutOrStdout(), "API token: %s\n", resolvedToken)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "API token: supplied via %s\n", source)
				}
			}
			errCh := make(chan error, 1)
			go func() { errCh <- srv.Serve(l) }()
			select {
			case err := <-errCh:
				return err
			case <-cmd.Context().Done():
				shutdownCtx, cancel := contextWithTimeout(10 * time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "listen address; port 0 picks a free port")
	cmd.Flags().StringVar(&token, "token", "", "API bearer token (must be at least 16 characters)")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "", "environment variable containing the API token")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file containing the API token")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "allow binding a non-loopback address (use a TLS reverse proxy)")
	return cmd
}

func resolveToken(token, envName, file string) (string, bool, string, error) {
	set := 0
	if token != "" {
		set++
	}
	if envName != "" {
		set++
	}
	if file != "" {
		set++
	}
	if set > 1 {
		return "", false, "", fmt.Errorf("set only one of --token, --token-env and --token-file")
	}
	if token != "" {
		if len(token) < 16 {
			return "", false, "", fmt.Errorf("token must be at least 16 characters")
		}
		return token, false, "flag", nil
	}
	name := envName
	if name == "" && os.Getenv("WML_UPDATE_TOKEN") != "" {
		name = "WML_UPDATE_TOKEN"
	}
	if name != "" {
		v := os.Getenv(name)
		if len(v) < 16 {
			return "", false, "", fmt.Errorf("%s must contain a token of at least 16 characters", name)
		}
		return v, false, "environment " + name, nil
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", false, "", fmt.Errorf("read token file: %w", err)
		}
		v := strings.TrimSpace(string(data))
		if len(v) < 16 {
			return "", false, "", fmt.Errorf("token file must contain at least 16 characters")
		}
		return v, false, "file " + file, nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false, "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), true, "generated", nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func contextWithTimeout(d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func (a *app) publishCmd() *cobra.Command {
	var (
		specPath     string
		outDir       string
		keyPath      string
		baseURL      string
		manifestName string
	)
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Build a self-hosted manifest, checksums and signatures",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireNonEmpty("spec", specPath); err != nil {
				return usageError(err.Error())
			}
			res, err := publish.Publish(specPath, publish.Options{
				OutDir: outDir, SigningKey: keyPath, BaseURL: baseURL, ManifestName: manifestName,
			})
			if err != nil {
				return &engine.Error{Code: "publish", Err: err}
			}
			if a.jsonOut {
				return a.emitObject(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Manifest:  %s\n", res.ManifestPath)
			if res.SignaturePath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Signature: %s\n", res.SignaturePath)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Checksums: %s\n", res.ChecksumsPath)
			for _, asset := range res.Assets {
				fmt.Fprintf(cmd.OutOrStdout(), "Asset:     %s\n", asset)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&specPath, "spec", "", "publish spec JSON")
	cmd.Flags().StringVar(&outDir, "out", "dist/update", "output directory for manifest and artifacts")
	cmd.Flags().StringVar(&keyPath, "key", "", "Ed25519 private key (PKCS#8 PEM) for detached signatures")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "public base URL prepended to asset URLs")
	cmd.Flags().StringVar(&manifestName, "manifest-name", "manifest.json", "manifest filename")
	_ = cmd.MarkFlagRequired("spec")
	return cmd
}

func (a *app) keygenCmd() *cobra.Command {
	var (
		outDir string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate an Ed25519 signing key pair",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := os.MkdirAll(outDir, 0o700); err != nil {
				return err
			}
			privPath := filepath.Join(outDir, "signing-key.pem")
			pubPath := filepath.Join(outDir, "signing-key.pub.pem")
			if !force {
				for _, p := range []string{privPath, pubPath} {
					if _, err := os.Stat(p); err == nil {
						return fmt.Errorf("%s already exists; use --force to overwrite", p)
					}
				}
			}
			priv, pub, err := sign.Generate()
			if err != nil {
				return err
			}
			privPEM, err := sign.EncodePrivatePEM(priv)
			if err != nil {
				return err
			}
			pubPEM, err := sign.EncodePublicPEM(pub)
			if err != nil {
				return err
			}
			if err := writeFileAtomic(privPath, privPEM, 0o600); err != nil {
				return err
			}
			if err := writeFileAtomic(pubPath, pubPEM, 0o644); err != nil {
				return err
			}
			if a.jsonOut {
				return a.emitObject(map[string]string{
					"private_key":       privPath,
					"public_key":        pubPath,
					"public_key_base64": base64.StdEncoding.EncodeToString(pub),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Private key: %s\n", privPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Public key:  %s\n", pubPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Public key (base64): %s\n", base64.StdEncoding.EncodeToString(pub))
			return nil
		},
	}
	cmd.Flags().StringVarP(&outDir, "out-dir", "o", ".", "directory for signing-key.pem and signing-key.pub.pem")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing key files")
	return cmd
}

func (a *app) signCmd() *cobra.Command {
	var keyPath, outPath string
	cmd := &cobra.Command{
		Use:   "sign FILE",
		Short: "Create a detached Ed25519 signature for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if keyPath == "" {
				return usageError("--key is required")
			}
			priv, err := sign.LoadPrivateKey(keyPath)
			if err != nil {
				return &engine.Error{Code: "config", Err: err}
			}
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			sig := sign.Sign(priv, data)
			if outPath == "" {
				outPath = args[0] + ".sig"
			}
			if err := writeFileAtomic(outPath, []byte(sig+"\n"), 0o644); err != nil {
				return err
			}
			if a.jsonOut {
				return a.emitObject(map[string]string{"file": args[0], "signature": outPath})
			}
			fmt.Fprintln(cmd.OutOrStdout(), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&keyPath, "key", "", "Ed25519 private key (PKCS#8 PEM)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "signature output path (default FILE.sig)")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

func (a *app) verifyCmd() *cobra.Command {
	var (
		sigPath       string
		publicKey     string
		publicKeyB64  string
		parseManifest bool
	)
	cmd := &cobra.Command{
		Use:   "verify FILE",
		Short: "Verify a detached Ed25519 signature",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if sigPath == "" {
				sigPath = args[0] + ".sig"
			}
			sigData, err := os.ReadFile(sigPath)
			if err != nil {
				return err
			}
			inline := publicKeyB64
			if inline == "" && publicKey == "" {
				cfg, err := a.loadConfig()
				if err == nil {
					if cfg.Security.PublicKey != "" {
						inline = cfg.Security.PublicKey
					} else if cfg.Security.PublicKeyFile != "" {
						publicKey = cfg.Security.PublicKeyFile
					}
				}
			}
			pub, err := sign.LoadPublicKey(inline, publicKey)
			if err != nil {
				return &engine.Error{Code: "config", Err: err}
			}
			if len(pub) == 0 {
				return usageError("no public key configured; use --public-key-file or set security.public_key_file")
			}
			if err := sign.Verify(pub, data, string(sigData)); err != nil {
				return &engine.Error{Code: "integrity", Err: err}
			}
			var parsed *manifest.Manifest
			if parseManifest {
				parsed, err = manifest.Parse(data)
				if err != nil {
					return &engine.Error{Code: "integrity", Err: err}
				}
			}
			if a.jsonOut {
				out := map[string]any{"ok": true, "file": args[0], "signature": sigPath}
				if parsed != nil {
					out["manifest"] = parsed
				}
				return a.emitObject(out)
			}
			if parsed != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Signature valid. Manifest %s %s (%d assets).\n",
					parsed.AppID, parsed.Version, len(parsed.Assets))
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Signature valid.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sigPath, "signature", "", "signature file (default FILE.sig)")
	cmd.Flags().StringVar(&publicKey, "public-key-file", "", "Ed25519 public key file (defaults to config)")
	cmd.Flags().StringVar(&publicKeyB64, "public-key-base64", "", "base64 raw Ed25519 public key")
	cmd.Flags().BoolVar(&parseManifest, "manifest", false, "also validate the file as an update manifest")
	return cmd
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
