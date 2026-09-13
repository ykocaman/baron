# Formula/baron.rb. Lives here until it moves into the
# baron-cli/homebrew-baron tap repo.
class Baron < Formula
  desc "Local-first AI coding agent orchestrator"
  homepage "https://baron.dev"
  url "https://github.com/baron-cli/baron/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "" # filled in by the release process once v0.1.0 is tagged
  license "MIT"

  depends_on "go" => :build
  depends_on "git"
  depends_on "bd"
  depends_on "gitleaks"
  depends_on "hunk"

  def install
    ldflags = %W[
      -s -w
      -X main.version=#{version}
      -X main.commit=#{tap.user}
      -X main.date=#{time.iso8601}
    ]
    system "go", "build", *std_go_args(ldflags: ldflags), "./cmd/baron"
    pkgshare.install "config/default.toml"
    pkgshare.install "scripts/brew/Brewfile.recommended"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/baron --version")
  end
end
