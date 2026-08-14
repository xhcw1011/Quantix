// One-off: mint a short-lived JWT for a given user so an ops script can call
// the authenticated engine start/stop API directly, without needing that
// user's actual password. Reads QUANTIX_JWT_SECRET from the environment.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Quantix/quantix/internal/api"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mint-token <user_id> <username>")
		os.Exit(1)
	}
	userID, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad user_id: %v\n", err)
		os.Exit(1)
	}
	secret := os.Getenv("QUANTIX_JWT_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "QUANTIX_JWT_SECRET required")
		os.Exit(1)
	}
	tok, err := api.GenerateToken(userID, os.Args[2], secret, 10*time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate token: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(tok)
}
