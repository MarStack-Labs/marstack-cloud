#!/bin/sh
set -eu

ROOT="${MARSTACK_RUNTIME_ROOT:-/var/lib/marstack}"
IMAGES="$ROOT/images"
RELEASE="${UBUNTU_RELEASE:-24.04}"

die() {
	echo "stage-images: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is not installed"
}

case "$(uname -s)" in
Linux) ;;
*) die "run this on the node, not on your workstation" ;;
esac

[ "$(id -u)" -eq 0 ] || die "run as root, the runtime root is only writable by root"

need curl
need gunzip
need file

case "$(uname -m)" in
aarch64)
	ARCH=arm64
	KERNEL_NAME=kernel.Image
	;;
x86_64)
	ARCH=amd64
	KERNEL_NAME=kernel.Image
	;;
*) die "unsupported architecture $(uname -m)" ;;
esac

DISK="$IMAGES/ubuntu-$RELEASE.qcow2"
KERNEL="$IMAGES/$KERNEL_NAME"
URL="https://cloud-images.ubuntu.com/releases/$RELEASE/release/ubuntu-$RELEASE-server-cloudimg-$ARCH.img"

mkdir -p "$IMAGES"

if [ -s "$DISK" ]; then
	echo "vm disk already staged: $DISK"
else
	echo "downloading $URL"
	if [ -t 2 ]; then
		curl -fL --progress-bar -o "$DISK.part" "$URL" || die "download failed"
	else
		curl -fL --silent --show-error -o "$DISK.part" "$URL" || die "download failed"
	fi
	mv "$DISK.part" "$DISK"
	echo "staged $DISK"
fi

file "$DISK" | grep -q QCOW || die "$DISK is not a qcow2 image"

if [ -s "$KERNEL" ]; then
	echo "microvm kernel already staged: $KERNEL"
else
	BOOT="/boot/vmlinuz-$(uname -r)"
	[ -r "$BOOT" ] || die "cannot read $BOOT, so there is no kernel to extract"

	echo "extracting $BOOT"
	gunzip -c "$BOOT" > "$KERNEL.part" || die "$BOOT is not gzip compressed, extract it by hand"
	mv "$KERNEL.part" "$KERNEL"
	echo "staged $KERNEL"
fi

file "$KERNEL" | grep -q "kernel .* boot executable Image" ||
	die "$KERNEL is not an uncompressed kernel image, which is what a microvm boots"

chmod 0644 "$DISK" "$KERNEL"

echo
echo "images on this node:"
ls -lh "$IMAGES"
echo
echo "vm instances take the file name without .qcow2, so use --image ubuntu-$RELEASE"
