fn main() {
    slint_build::compile("ui/main.slint").expect("compile Slint UI");

    #[cfg(windows)]
    generate_freerdp_bindings();
}

#[cfg(windows)]
fn generate_freerdp_bindings() {
    use std::env;
    use std::path::PathBuf;

    println!("cargo:rerun-if-changed=native/freerdp_wrapper.h");
    println!("cargo:rerun-if-env-changed=VCPKG_ROOT");
    println!("cargo:rerun-if-env-changed=VCPKGRS_TRIPLET");

    let freerdp = vcpkg::Config::new()
        .emit_includes(true)
        .find_package("freerdp")
        .expect("FreeRDP was not found; install freerdp[client]:x64-windows-static with vcpkg");

    let mut builder = bindgen::Builder::default()
        .header("native/freerdp_wrapper.h")
        .allowlist_function("freerdp_.*")
        .allowlist_function("gdi_.*")
        .allowlist_type(".*")
        .allowlist_var("CHUZI_.*")
        .layout_tests(false)
        .generate_comments(false)
        .clang_arg("-DWIN32_LEAN_AND_MEAN");

    for include_path in freerdp.include_paths {
        builder = builder.clang_arg(format!("-I{}", include_path.display()));
        for versioned_include in ["freerdp3", "winpr3"] {
            let path = include_path.join(versioned_include);
            if path.is_dir() {
                builder = builder.clang_arg(format!("-I{}", path.display()));
            }
        }
    }

    // vcpkg-rs emits the FreeRDP/WinPR package libraries, but it cannot infer
    // the Windows SDK libraries that are part of FreeRDP's CMake interface.
    // Keep those explicit so the static triplet links on the MSVC runner.
    for library in [
        "advapi32", "bcrypt", "cfgmgr32", "credui", "crypt32", "gdi32", "ncrypt", "ntdsapi",
        "rpcrt4", "secur32", "shlwapi", "shell32", "user32", "ws2_32",
    ] {
        println!("cargo:rustc-link-lib=dylib={library}");
    }

    let bindings = builder
        .generate()
        .expect("unable to generate FreeRDP bindings");
    let output = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR is not set"));
    bindings
        .write_to_file(output.join("freerdp_bindings.rs"))
        .expect("unable to write FreeRDP bindings");
}
