# Homebrew formula template for the Sapu tap (<TAP>).
#
# Copy this file to Formula/sapu.rb in the tap repository after each STABLE
# release and replace the two values below. Do not publish betas to the tap.
#   VERSION  the release version without the "v", for example 1.0.0
#   SHA256   the line for sapu_<VERSION>_darwin_universal.tar.gz in the
#            release's checksums.txt
# Steps: docs/releasing.md.
class Sapu < Formula
  desc "Mac cleaner that never deletes anything you can't undo"
  homepage "https://<OFFICIAL-DOMAIN>"
  version "<VERSION>"
  url "https://github.com/khmuhtadin/sapu/releases/download/v<VERSION>/sapu_<VERSION>_darwin_universal.tar.gz"
  sha256 "<SHA256>"
  license "Apache-2.0"

  depends_on :macos

  def install
    bin.install "sapu"
    doc.install "README.md", "NOTICE", "THIRD_PARTY_LICENSES"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/sapu version")
  end
end
