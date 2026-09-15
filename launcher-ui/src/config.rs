use std::{
    env,
    ffi::OsString,
    path::{Path, PathBuf},
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LauncherConfig {
    pub launcher: PathBuf,
    pub root: PathBuf,
    pub manifest: PathBuf,
    pub release_index: Option<String>,
    pub update_manifest: Option<PathBuf>,
    pub source_root: Option<PathBuf>,
    pub download_dir: Option<PathBuf>,
    pub trusted_signers: Vec<String>,
    pub allow_http_loopback: bool,
}

impl LauncherConfig {
    pub fn from_env() -> Result<Option<Self>, String> {
        let executable = env::current_exe().map_err(|_| "launcher_path_unavailable".to_owned())?;
        let executable_dir = executable
            .parent()
            .ok_or_else(|| "launcher_path_unavailable".to_owned())?
            .to_path_buf();
        Self::from_args(env::args_os().skip(1), executable_dir)
    }

    pub fn from_args(
        arguments: impl IntoIterator<Item = OsString>,
        executable_dir: PathBuf,
    ) -> Result<Option<Self>, String> {
        let mut root: Option<PathBuf> = None;
        let mut launcher: Option<PathBuf> = None;
        let mut manifest: Option<PathBuf> = None;
        let mut release_index = None;
        let mut update_manifest = None;
        let mut source_root = None;
        let mut download_dir = None;
        let mut trusted_signers = Vec::new();
        let mut allow_http_loopback = false;
        let mut arguments = arguments.into_iter();

        while let Some(argument) = arguments.next() {
            let name = argument.to_string_lossy();
            match name.as_ref() {
                "--help" | "-h" => {
                    println!(
                        "usage: chuzi-launcher-ui [--root PATH] [--launcher PATH] [--manifest PATH] [--release-index URL] [--update-manifest PATH] [--source-root PATH] [--download-dir PATH] [--trusted-signers CSV] [--allow-http-loopback]"
                    );
                    return Ok(None);
                }
                "--root" => root = Some(next_path(&mut arguments, "--root")?),
                "--launcher" => launcher = Some(next_path(&mut arguments, "--launcher")?),
                "--manifest" => manifest = Some(next_path(&mut arguments, "--manifest")?),
                "--source-root" => source_root = Some(next_path(&mut arguments, "--source-root")?),
                "--download-dir" => {
                    download_dir = Some(next_path(&mut arguments, "--download-dir")?)
                }
                "--update-manifest" => {
                    update_manifest = Some(next_path(&mut arguments, "--update-manifest")?)
                }
                "--trusted-signers" => {
                    let value = arguments.next().ok_or_else(|| {
                        "--trusted-signers requires a comma-separated list".to_owned()
                    })?;
                    trusted_signers = value
                        .to_string_lossy()
                        .split(',')
                        .map(str::trim)
                        .filter(|signer| !signer.is_empty())
                        .map(str::to_owned)
                        .collect();
                }
                "--release-index" => {
                    release_index = Some(
                        arguments
                            .next()
                            .ok_or_else(|| "--release-index requires a URL".to_owned())?
                            .to_string_lossy()
                            .into_owned(),
                    );
                }
                "--allow-http-loopback" => allow_http_loopback = true,
                other => return Err(format!("unknown option: {other}")),
            }
        }

        let root = root.unwrap_or_else(|| executable_dir.clone());
        let launcher_default = if cfg!(target_os = "windows") {
            "chuzi-launcher.exe"
        } else {
            "chuzi-launcher"
        };
        let launcher = launcher.unwrap_or_else(|| executable_dir.join(launcher_default));
        let manifest = manifest.unwrap_or_else(|| root.join("release-manifest.json"));
        let config = Self {
            launcher,
            root,
            manifest,
            release_index,
            update_manifest,
            source_root,
            download_dir,
            trusted_signers,
            allow_http_loopback,
        };
        config.validate()?;
        Ok(Some(config))
    }

    pub fn validate(&self) -> Result<(), String> {
        validate_absolute("launcher", &self.launcher)?;
        validate_absolute("root", &self.root)?;
        validate_absolute("manifest", &self.manifest)?;
        for (name, path) in [
            ("update_manifest", self.update_manifest.as_ref()),
            ("source_root", self.source_root.as_ref()),
            ("download_dir", self.download_dir.as_ref()),
        ] {
            if let Some(path) = path {
                validate_absolute(name, path)?;
            }
        }
        if self.allow_http_loopback && self.release_index.is_none() {
            return Err("allow_http_loopback requires release_index".to_owned());
        }
        Ok(())
    }

    pub fn common_args(&self) -> Vec<OsString> {
        let mut args = vec![
            OsString::from("-manifest"),
            self.manifest.as_os_str().to_owned(),
            OsString::from("-root"),
            self.root.as_os_str().to_owned(),
        ];
        if let Some(source_root) = &self.source_root {
            args.extend([
                OsString::from("-source-root"),
                source_root.as_os_str().to_owned(),
            ]);
        }
        if let Some(download_dir) = &self.download_dir {
            args.extend([
                OsString::from("-download-dir"),
                download_dir.as_os_str().to_owned(),
            ]);
        }
        if let Some(release_index) = &self.release_index {
            args.extend([
                OsString::from("-release-index"),
                OsString::from(release_index),
            ]);
        }
        if let Some(update_manifest) = &self.update_manifest {
            args.extend([
                OsString::from("-update-manifest"),
                update_manifest.as_os_str().to_owned(),
            ]);
        }
        if !self.trusted_signers.is_empty() {
            args.extend([
                OsString::from("-trusted-signers"),
                OsString::from(self.trusted_signers.join(",")),
            ]);
        }
        if self.allow_http_loopback {
            args.push(OsString::from("-allow-http-loopback"));
        }
        args
    }
}

fn next_path(
    arguments: &mut impl Iterator<Item = OsString>,
    option: &str,
) -> Result<PathBuf, String> {
    arguments
        .next()
        .map(PathBuf::from)
        .ok_or_else(|| format!("{option} requires a path"))
}

fn validate_absolute(name: &str, path: &Path) -> Result<(), String> {
    if !path.is_absolute() || path.to_string_lossy().trim().is_empty() {
        return Err(format!("{name} must be an absolute path"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn executable_dir() -> PathBuf {
        #[cfg(windows)]
        {
            PathBuf::from(r"C:\opt\chuzi")
        }
        #[cfg(not(windows))]
        {
            PathBuf::from("/opt/chuzi")
        }
    }

    #[test]
    fn parses_defaults_relative_to_executable_dir() {
        let executable_dir = executable_dir();
        let config = LauncherConfig::from_args(Vec::new(), executable_dir.clone())
            .expect("config")
            .expect("config value");
        assert_eq!(config.root, executable_dir);
        assert_eq!(
            config.launcher,
            executable_dir.join(if cfg!(windows) {
                "chuzi-launcher.exe"
            } else {
                "chuzi-launcher"
            })
        );
        assert_eq!(
            config.manifest,
            executable_dir.join("release-manifest.json")
        );
    }

    #[test]
    fn rejects_unknown_options_and_invalid_loopback_configuration() {
        let error = LauncherConfig::from_args(vec![OsString::from("--unknown")], executable_dir())
            .expect_err("unknown option should fail");
        assert!(error.contains("unknown option"));

        let config = LauncherConfig {
            launcher: executable_dir().join(if cfg!(windows) {
                "chuzi-launcher.exe"
            } else {
                "chuzi-launcher"
            }),
            root: executable_dir(),
            manifest: executable_dir().join("release-manifest.json"),
            release_index: None,
            update_manifest: None,
            source_root: None,
            download_dir: None,
            trusted_signers: Vec::new(),
            allow_http_loopback: true,
        };
        assert!(config.validate().is_err());
    }
}
