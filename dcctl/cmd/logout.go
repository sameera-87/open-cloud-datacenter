package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear stored credentials",
		Long:  `Remove the saved access and refresh tokens from ~/.dcctl/credentials.json.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("locate home directory: %w", err)
			}
			path := filepath.Join(home, ".dcctl", "credentials.json")
			if err := os.Remove(path); os.IsNotExist(err) {
				fmt.Println("Not logged in.")
				return nil
			} else if err != nil {
				return fmt.Errorf("remove credentials: %w", err)
			}
			fmt.Println("Logged out. Run `dcctl login` to authenticate again.")
			return nil
		},
	}
}
