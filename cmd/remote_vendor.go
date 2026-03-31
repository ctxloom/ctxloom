package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/remote"
)

var remoteVendorCmd = &cobra.Command{
	Use:   "vendor",
	Short: "Copy all remote dependencies locally",
	Long: `Vendor all locked remote dependencies to .ctxloom/vendor/.

This copies all items from the lockfile to a local vendor directory,
enabling offline use and ensuring reproducible builds without network access.

Examples:
  ctxloom remote vendor              # Copy all deps to .ctxloom/vendor/
  ctxloom remote vendor --enable     # Enable vendor mode (use vendored deps)
  ctxloom remote vendor --disable    # Disable vendor mode (fetch from remote)`,
	RunE: runRemoteVendor,
}

var vendorEnable bool
var vendorDisable bool

func runRemoteVendor(cmd *cobra.Command, args []string) error {
	vendorManager := remote.NewVendorManager(".ctxloom")

	// Handle enable/disable flags
	if vendorEnable {
		if err := vendorManager.SetVendorMode(true); err != nil {
			return err
		}
		fmt.Println("Vendor mode enabled. Will use .ctxloom/vendor/ for dependencies.")
		return nil
	}

	if vendorDisable {
		if err := vendorManager.SetVendorMode(false); err != nil {
			return err
		}
		fmt.Println("Vendor mode disabled. Will fetch from remotes.")
		return nil
	}

	// Vendor all dependencies
	lockManager := remote.NewLockfileManager(".ctxloom")
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("No entries in lockfile. Generate one with: ctxloom remote lock")
		return nil
	}

	registry, err := remote.NewRegistry("")
	if err != nil {
		return err
	}

	auth := remote.LoadAuth("")

	fmt.Printf("Vendoring %d dependencies...\n", lockfile.Count())

	if err := vendorManager.VendorAll(cmd.Context(), lockfile, registry, auth); err != nil {
		return err
	}

	fmt.Printf("\nVendored to %s\n", vendorManager.VendorDir())
	fmt.Println("Enable vendor mode with: ctxloom remote vendor --enable")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteVendorCmd)

	remoteVendorCmd.Flags().BoolVar(&vendorEnable, "enable", false,
		"Enable vendor mode (use vendored dependencies)")
	remoteVendorCmd.Flags().BoolVar(&vendorDisable, "disable", false,
		"Disable vendor mode (fetch from remotes)")
}
