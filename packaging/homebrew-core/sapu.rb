class Sapu < Formula
  desc "Mac cleaner that never deletes anything you can't undo"
  homepage "https://github.com/khmuhtadin/sapu"
  url "https://github.com/khmuhtadin/sapu/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "61b04d2dd5457bac1e99359448fa5060178ae0754b7276d876074f037f4b37a8"
  license "Apache-2.0"
  head "https://github.com/khmuhtadin/sapu.git", branch: "main"

  depends_on "go" => :build
  depends_on :macos

  def install
    ldflags = "-X github.com/khmuhtadin/sapu/internal/cli.Version=#{version}"
    system "go", "build", *std_go_args(ldflags:), "./cmd/sapu"
    generate_completions_from_executable(bin/"sapu", "completion")
  end

  test do
    assert_match "sapu #{version}", shell_output("#{bin}/sapu version")

    # HOME is testpath here. A plan finds the npm cache and moves nothing.
    (testpath/".npm/_cacache/entry").write "x" * 4096
    system bin/"sapu", "clean", "--plan", testpath/"plan.json"
    assert_match ".npm/_cacache", (testpath/"plan.json").read
    assert_path_exists testpath/".npm/_cacache/entry"
  end
end
