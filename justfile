# Set default variables
IPU_BOOTC_BUILDER_URL := env("IPU_BOOTC_BUILDER_URL","quay.io/sadasilv/ipu-rhel-iso-builder:test")
BOOTC_IMAGE_URL := env("BOOTC_IMAGE_URL","quay.io/centos-bootc/centos-bootc:stream9")

# Build the container image for aarch64
build:
	sudo podman build \
	  --security-opt label=type:unconfined_t \
	  --platform linux/arm64 \
	  -t {{IPU_BOOTC_BUILDER_URL}} .

# Push the image to the registry
push:
	sudo podman push {{IPU_BOOTC_BUILDER_URL}}

# Run the container interactively for building the ISO
run:
	sudo podman run --privileged \
	  --security-opt label=type:unconfined_t \
	  -it \
	  --arch aarch64 \
	  -v ${PWD}/workdir:/workdir \
	  {{IPU_BOOTC_BUILDER_URL}} \
	  -u {{BOOTC_IMAGE_URL}}

# Full workflow: build, push, run
all: build push run
