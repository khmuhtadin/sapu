# The Homebrew formula of the tap khmuhtadin/homebrew-sapu
# (brew install khmuhtadin/sapu/sapu). After each release, update the version
# in url (twice) and sha256 (from the release's checksums.txt), then copy this
# file to Formula/sapu.rb in the tap. Steps: docs/releasing.md.
class Sapu < Formula
  desc "Mac cleaner that never deletes anything you can't undo"
  homepage "https://github.com/khmuhtadin/sapu"
  url "https://github.com/khmuhtadin/sapu/releases/download/v0.2.0/sapu_0.2.0_darwin_universal.tar.gz"
  sha256 "3b8cf5927e814fbea95ce953777084b4f92c3c90c5824a69016adbb76e249fa2"
  license "Apache-2.0"

  depends_on :macos

  def install
    bin.install "sapu"
    prefix.install "THIRD_PARTY_LICENSES", "THIRD_PARTY_NOTICES"
  end

  test do
    assert_match "sapu #{version}", shell_output("#{bin}/sapu version")
  end
end
