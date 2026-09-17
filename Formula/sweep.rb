class Sweep < Formula
  desc "Interactive macOS disk cleaner with a cached file tree"
  homepage "https://github.com/jacobslunga/sweep"
  version "0.3.1"

  depends_on :macos

  on_arm do
    url "https://github.com/jacobslunga/sweep/releases/download/v0.3.1/sweep_0.3.1_darwin_arm64.tar.gz"
    sha256 "f13f8732a485607ccb583d27e942a42c29d46c55267ea0d159953232df0565e8"
  end

  on_intel do
    url "https://github.com/jacobslunga/sweep/releases/download/v0.3.1/sweep_0.3.1_darwin_amd64.tar.gz"
    sha256 "c3a665f5022066c32f1605337d7fb2797d66d8d754e7176d8701883fd12426a5"
  end

  def install
    bin.install "sweep"
  end

  test do
    assert_equal "sweep #{version}", shell_output("#{bin}/sweep --version").strip
  end
end
