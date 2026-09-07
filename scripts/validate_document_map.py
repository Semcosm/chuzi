#!/usr/bin/env python3
"""Validate that README's Document Map is generated from its tree config."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from generate_document_map import main as generate_main


if __name__ == "__main__":
    sys.argv.insert(1, "--check")
    sys.exit(generate_main())
