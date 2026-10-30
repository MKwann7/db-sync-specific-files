package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/MKwann7/db-sync-specific-files/internal/auth"
)

func runAuthCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dbsync auth <login|status>")
	}
	switch args[0] {
	case "login":
		return runAuthLogin(args[1:])
	case "status":
		return runAuthStatus(args[1:])
	default:
		return fmt.Errorf("unknown auth subcommand %q (expected 'login' or 'status')", args[0])
	}
}

func runAuthLogin(args []string) error {
	fs := flag.NewFlagSet("auth login", flag.ExitOnError)
	port := fs.Int("port", 53682, "local loopback port for the OAuth redirect callback")
	manual := fs.Bool("manual", false, "use the manual authorization-code flow instead of a local callback listener (for headless/remote environments)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var tokens auth.TokenSet
	if *manual {
		state, err := auth.GenerateState()
		if err != nil {
			return err
		}
		authURL := auth.AuthorizationURL(creds, state, "")
		fmt.Println("1. Open this URL in a browser and approve access:")
		fmt.Println()
		fmt.Println("   " + authURL)
		fmt.Println()
		fmt.Println("2. Dropbox will display an authorization code on the page.")
		fmt.Print("3. Paste the authorization code here and press Enter: ")

		var code string
		if _, err := fmt.Scanln(&code); err != nil {
			return fmt.Errorf("reading authorization code: %w", err)
		}
		tokens, err = auth.ManualExchange(ctx, creds, code)
		if err != nil {
			return fmt.Errorf("exchanging authorization code: %w", err)
		}
	} else {
		result, err := auth.RunLocalCallbackLogin(ctx, creds, *port, func(url string) {
			fmt.Println("Open this URL in a browser to authorize dbsync with Dropbox:")
			fmt.Println()
			fmt.Println("  " + url)
			fmt.Println()
			fmt.Println("Waiting for authorization... (Ctrl+C to cancel, or re-run with --manual if no browser is reachable)")
		})
		if err != nil {
			return err
		}
		tokens = result.Tokens
	}

	if tokens.RefreshToken == "" {
		return fmt.Errorf("dropbox did not return a refresh token; ensure the app is configured for offline access and try again")
	}

	fmt.Println()
	fmt.Println("Authorization successful.")
	fmt.Println()
	fmt.Println("Add the following to your .env file (never commit this file):")
	fmt.Println()
	fmt.Printf("  DROPBOX_REFRESH_TOKEN=%s\n", tokens.RefreshToken)
	fmt.Println()
	fmt.Println("The access token itself is short-lived and is never persisted; dbsync will")
	fmt.Println("automatically refresh it using the refresh token above for unattended operation.")
	return nil
}

func runAuthStatus(args []string) error {
	creds, err := loadCredentials()
	if err != nil {
		fmt.Println("Dropbox app credentials: NOT CONFIGURED")
		return err
	}
	fmt.Println("Dropbox app credentials: configured (DROPBOX_APP_KEY / DROPBOX_APP_SECRET present)")

	refreshToken := os.Getenv("DROPBOX_REFRESH_TOKEN")
	if refreshToken == "" {
		fmt.Println("Refresh token: NOT CONFIGURED (run 'dbsync auth login')")
		return nil
	}
	fmt.Println("Refresh token: configured")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ts, err := auth.RefreshAccessToken(ctx, creds, refreshToken)
	if err != nil {
		if oauthErr, ok := err.(*auth.OAuthError); ok && oauthErr.IsInvalidGrant() {
			fmt.Println("Refresh token status: INVALID / REVOKED - run 'dbsync auth login' again")
			return nil
		}
		return fmt.Errorf("checking refresh token: %w", err)
	}
	fmt.Printf("Refresh token status: VALID (access token refreshed successfully, expires in %s)\n", time.Until(ts.ExpiresAt).Round(time.Second))
	return nil
}
