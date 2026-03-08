package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/SophisticatedContextManager/scm/internal/remote"
)

var remoteVendorCmd = &cobra.Command{
	Use:   "vendor",
	Short: "Copy all remote dependencies locally",
	Long: `Vendor all locked remote dependencies to .scm/vendor/.

This copies all items from the lockfile to a local vendor directory,
enabling offline use and ensuring reproducible builds without network access.

Examples:
  scm remote vendor              # Copy all deps to .scm/vendor/
  scm remote vendor --enable     # Enable vendor mode (use vendored deps)
  scm remote vendor --disable    # Disable vendor mode (fetch from remote)`,
	RunE: runRemoteVendor,
}

var vendorEnable bool
var vendorDisable bool

func runRemoteVendor(cmd *cobra.Command, args []string) error {
	vendorManager := remote.NewVendorManager(".scm")

	// Handle enable/disable flags
	if vendorEnable {
		if err := vendorManager.SetVendorMode(true); err != nil {
			return err
		}
		fmt.Println("Vendor mode enabled. Will use .scm/vendor/ for dependencies.")
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
	lockManager := remote.NewLockfileManager(".scm")
	lockfile, err := lockManager.Load()
	if err != nil {
		return err
	}

	if lockfile.IsEmpty() {
		fmt.Println("No entries in lockfile. Generate one with: scm remote lock")
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
	fmt.Println("Enable vendor mode with: scm remote vendor --enable")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteVendorCmd)

	remoteVendorCmd.Flags().BoolVar(&vendorEnable, "enable", false,
		"Enable vendor mode (use vendored dependencies)")
	remoteVendorCmd.Flags().BoolVar(&vendorDisable, "disable", false,
		"Disable vendor mode (fetch from remotes)")
}
