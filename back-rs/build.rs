fn main() -> std::io::Result<()> {
    println!("cargo:rerun-if-changed=../core/api/quotes.proto");
    prost_build::compile_protos(&["../core/api/quotes.proto"], &["../core/api"])
}
