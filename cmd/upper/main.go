// Command upper is a terminal Discord client for text chat: servers,
// channels and direct messages.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lengh/upper/internal/config"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/discord/discordtest"
	"github.com/lengh/upper/internal/state"
	"github.com/lengh/upper/internal/ui"
	"golang.org/x/term"
)

var version = "dev"

const tosWarning = `upper logs in with your personal Discord account token.

Discord's Terms of Service do not allow third-party clients or automating
user accounts. Accounts using unofficial clients have occasionally been
flagged or banned. upper behaves like a person typing (no scraping, no
automation, strict rate limiting), but the risk is never zero.
`

const tokenHelp = `How to get your token (keep it secret: it grants full access):
  1. Open https://discord.com/app in a browser and log in.
  2. Press F12, open the Network tab and type "api" in the filter.
  3. Click any channel, select a request and find the "Authorization"
     request header. Its value is your token.
`

func main() {
	cfgPath := flag.String("config", "", "path to config.json (default: ~/.config/upper/config.json)")
	logout := flag.Bool("logout", false, "delete the stored token and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	demo := flag.Bool("demo", false, "try the interface against a built-in fake server (no Discord account used)")
	flag.Parse()

	if *showVersion {
		fmt.Println("upper", version)
		return
	}
	if *logout {
		if err := config.DeleteToken(); err != nil {
			fatal(err)
		}
		fmt.Println("Token removed.")
		return
	}

	// Endpoint overrides for testing against a local fake server.
	if u := os.Getenv("UPPER_API_URL"); u != "" {
		discord.APIBase = u
	}
	if u := os.Getenv("UPPER_GATEWAY_URL"); u != "" {
		discord.GatewayURL = u
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var token string
	if *demo {
		srv := discordtest.New("demo")
		defer srv.Close()
		srv.SeedDemo()
		go srv.Demo(ctx)
		discord.APIBase, discord.GatewayURL, token = srv.APIURL(), srv.GatewayURL(), "demo"
	} else if token, err = login(ctx); err != nil {
		fatal(err)
	}

	rest := discord.NewREST(token)
	gw := discord.NewGateway(token)
	app := ui.New(cfg, state.New(), rest, gw)
	if err := app.Run(ctx); err != nil {
		if errors.Is(err, discord.ErrAuth) {
			_ = config.DeleteToken()
			fatal(errors.New("Discord rejected the token (it was probably reset by a password change or logout). Run upper again to log in."))
		}
		fatal(err)
	}
}

// login returns a validated token from the environment, the token file, or
// an interactive prompt.
func login(ctx context.Context) (string, error) {
	if token, err := config.LoadToken(); err == nil && token != "" {
		return token, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no token: set UPPER_TOKEN or run upper interactively once")
	}

	fmt.Print(tosWarning, "\n", tokenHelp, "\n")
	for {
		fmt.Print("Token (input hidden): ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		token := strings.Trim(strings.TrimSpace(string(raw)), `"`)
		if token == "" {
			continue
		}

		vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		me, err := discord.NewREST(token).Me(vctx)
		cancel()
		if err != nil {
			if discord.IsUnauthorized(err) {
				fmt.Println("That token was rejected. Try again (Ctrl+C to quit).")
				continue
			}
			return "", fmt.Errorf("checking token: %w", err)
		}
		fmt.Printf("Logged in as %s.\n", me.Tag())

		fmt.Print("Save the token for next time? It is stored readable only by you. [Y/n] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a == "" || a == "y" || a == "yes" {
			path, err := config.SaveToken(token)
			if err != nil {
				fmt.Println("Could not save token:", err)
			} else {
				fmt.Println("Saved to", path)
			}
		}
		return token, nil
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "upper:", err)
	os.Exit(1)
}
