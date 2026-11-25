fn main() {
    // toml-cfg reads cfg.toml at compile time: rebuild when it changes.
    println!("cargo:rerun-if-changed=cfg.toml");
    embuild::espidf::sysenv::output();
}
