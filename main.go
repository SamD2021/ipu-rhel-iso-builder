package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/spf13/cobra"
)

type IsoBuilder struct {
	inputISO      string
	outputISO     string
	kickstartFile string
	bootcImage    string
	kernelArgs    string
	tempDir       string
	rhelVersion   string
	rootDir       string
}

func NewIsoBuilder() *IsoBuilder {
	return &IsoBuilder{
		kernelArgs:  "ip=192.168.0.2:::255.255.255.0::enp0s1f0:off netroot=iscsi:192.168.0.1::::iqn.e2000:acc acpi=force",
		rhelVersion: "9.6",
		rootDir:     "/workdir",
	}
}

func main() {
	ib := NewIsoBuilder()
	var rootCmd = &cobra.Command{
		Use:   "iso-builder",
		Short: "Build a customized RHEL Bootc ISO",
		Run: func(cmd *cobra.Command, args []string) {
			if err := ib.run(); err != nil {
				log.Fatalf("Error: %v", err)
			}
		},
	}

	rootCmd.Flags().StringVarP(&ib.inputISO, "input_iso", "i", "", "Path to input ISO")
	rootCmd.Flags().StringVarP(&ib.outputISO, "output_iso", "o", "output.iso", "Path to output ISO")
	rootCmd.Flags().StringVarP(&ib.kickstartFile, "kickstart", "k", "", "Path to kickstart file")
	rootCmd.Flags().StringVarP(&ib.bootcImage, "bootc_image", "u", "", "Bootc image URL or directory")
	rootCmd.Flags().StringVarP(&ib.kernelArgs, "kernel_args", "a", ib.kernelArgs, "Kernel arguments")
	rootCmd.Flags().StringVarP(&ib.rhelVersion, "rhel_version", "v", ib.rhelVersion, "RHEL ISO Version")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func (ib *IsoBuilder) run() error {
	for _, command := range []string{"mkksiso", "losetup", "skopeo"} {
		checkCommand(command)
	}

	if err := os.Chdir(ib.rootDir); err != nil {
		return fmt.Errorf("could not change to %s with error: %s", ib.rootDir, err)
	}

	if err := ensureLoopSupport(); err != nil {
		return fmt.Errorf("loop support failed: %s", err)
	}

	if arch := strings.TrimSpace(runCmdOutput("uname", "-m")); arch != "aarch64" {
		return fmt.Errorf("must run on aarch64 (got %s)", arch)
	}

	if _, err := os.Stat(ib.outputISO); err == nil {
		return fmt.Errorf("output ISO %s already exists", ib.outputISO)
	}

	fmt.Println("Fetching ISO...")
	imgErrCh := AsyncErr(ib.prepareContainerImage)
	isoErrCh := AsyncErr(ib.prepareInputIso)

	var imgDone, isoDone bool

	for !imgDone || !isoDone {
		select {
		case err := <-imgErrCh:
			imgDone = true
			if err != nil {
				return fmt.Errorf("container image preparation failed: %w", err)
			}
			fmt.Println("Done preparing container image!")

		case err := <-isoErrCh:
			isoDone = true
			if err != nil {
				return fmt.Errorf("ISO preparation failed: %w", err)
			}
			fmt.Println("Done fetching ISO!")
		}
	}

	if err := ib.prepareKickstart(); err != nil {
		return err
	}

	fmt.Println("Making tmp dir...")
	ib.tempDir = runCmdOutput("mktemp", "-d")
	ib.tempDir = strings.TrimSpace(ib.tempDir)
	defer os.RemoveAll(ib.tempDir)

	fmt.Printf("Copying input iso: %s and kickstartFile: %s in tempDir:%s...", ib.inputISO, ib.kickstartFile, ib.tempDir)
	runCmd("cp", ib.inputISO, ib.tempDir)
	runCmd("cp", ib.kickstartFile, ib.tempDir)
	ib.inputISO = path.Join(ib.tempDir, ib.inputISO)
	ib.kickstartFile = path.Join(ib.tempDir, ib.kickstartFile)
	ib.outputISO = path.Join("/workdir", ib.outputISO)
	fmt.Println("Generating ISO...")
	runCmd("mkksiso", "--ks", ib.kickstartFile, "-a", "/tmp/container", "-c", ib.kernelArgs, ib.inputISO, ib.outputISO)
	fmt.Println("Done.")
	return nil
}

func checkCommand(name string) {
	_, err := exec.LookPath(name)
	if err != nil {
		log.Fatalf("Required command %s not found in PATH", name)
	}
}

func runCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Command failed: %s %v", name, args)
	}
}

func runCmdOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		log.Fatalf("Command failed: %s %v", name, args)
	}
	return string(out)
}

func ensureLoopSupport() error {
	if _, err := os.Stat("/dev/loop-control"); os.IsNotExist(err) {
		return fmt.Errorf("/dev/loop-control missing. Are you in a privileged container?")
	}
	return nil
}

func (ib *IsoBuilder) prepareKickstart() error {
	if ib.kickstartFile == "" {
		if _, err := os.Stat("kickstart.ks"); err == nil {
			ib.kickstartFile = "kickstart.ks"
			return nil
		}

		fmt.Println("Generating default kickstart.ks")
		kickstart := fmt.Sprintf(`# Root Password
rootpw redhat
lang en_US.UTF-8
timezone America/New_York --utc
text
eula --agreed
skipx
clearpart --all --initlabel
autopart --type=lvm --noswap
bootloader --location=mbr --driveorder=sda --append="%s"
network --bootproto=dhcp --device=enp0s1f0d1
ostreecontainer --url=/run/install/repo/container --transport=oci --no-signature-verification
%%post
echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config
systemctl restart sshd.service
nmcli con modify enp0s1f0 ipv4.never-default yes
%%end
reboot`, ib.kernelArgs)
		error := os.WriteFile("kickstart.ks", []byte(kickstart), 0644)
		ib.kickstartFile = "kickstart.ks"
		return error
	}
	return nil
}

func (ib *IsoBuilder) prepareContainerImage() error {
	fmt.Println("Saving Bootc image to /tmp/container")
	runCmd("rm", "-rf", "/tmp/container")

	return exec.Command("skopeo", "copy",
		"--override-arch=arm64",
		fmt.Sprintf("docker://%s", ib.bootcImage),
		"oci:/tmp/container:latest").Run()
}

func (ib *IsoBuilder) prepareInputIso() error {
	if ib.inputISO == "" {
		versionBits := strings.Split(ib.rhelVersion, ".")
		if len(versionBits) != 2 {
			return fmt.Errorf("invalid RHEL version format: expected MAJOR.MINOR")
		}
		major := versionBits[0]
		minor := versionBits[1]
		downloadURL := fmt.Sprintf(
			"http://download.eng.bos.redhat.com/rhel-%s/nightly/RHEL-%s/latest-RHEL-%s.%s/compose/BaseOS/aarch64/iso/",
			major, major, major, minor)

		cmd := fmt.Sprintf(
			`curl -s %s | grep -oP 'href="\K[RHEL-]*[\d\.-]+aarch64-boot\.iso(?=")' | head -n1`,
			downloadURL)

		isoName := runCmdOutput("bash", "-c", cmd)
		ib.inputISO = strings.TrimSpace(isoName)
		fmt.Println(ib.inputISO)

		if ib.inputISO == "" {
			return fmt.Errorf("failed to extract ISO file name from %s", downloadURL)
		}

		if _, err := os.Stat(ib.inputISO); err != nil {
			runCmd("curl", "-O", downloadURL+ib.inputISO)
		}
	}
	return nil
}

func AsyncErr(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() {
		ch <- fn()
	}()
	return ch
}
