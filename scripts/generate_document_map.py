#!/usr/bin/env python3
"""Generate or check the README Document Map from its configured tree."""
import argparse
import json
import re
import sys
from pathlib import Path


def error(message):
    raise ValueError(message)


def validate_nodes(nodes, root, titles=None, paths=None):
    titles = set() if titles is None else titles
    paths = set() if paths is None else paths
    if not isinstance(nodes, list) or not nodes:
        error("nodes must be a non-empty array")
    for node in nodes:
        if not isinstance(node, dict):
            error("each node must be an object")
        for key in ("title", "title_zh"):
            if not isinstance(node.get(key), str) or not node[key].strip():
                error(f"each node must have a non-empty {key}")
        if node["title"] in titles:
            error(f"node titles must be unique: {node['title']}")
        titles.add(node["title"])
        path = node.get("path")
        children = node.get("children")
        if path is None and not children:
            error(f"node has neither path nor children: {node['title']}")
        if path is not None:
            path_obj = Path(path)
            if not isinstance(path, str) or not path or path_obj.is_absolute() or ".." in path_obj.parts:
                error(f"node path is not repository-relative: {path}")
            if path in paths:
                error(f"node paths must be unique: {path}")
            paths.add(path)
            if not (root / path).is_file():
                error(f"mapped path does not exist: {path}")
        if children is not None:
            validate_nodes(children, root, titles, paths)


def label(node):
    return f"{node['title']}（{node['title_zh']}）"


def render_nodes(nodes, depth=0):
    lines = []
    for node in nodes:
        prefix = "  " * depth + "- "
        if node.get("path"):
            lines.append(f"{prefix}[{label(node)}]({node['path']})")
        else:
            lines.append(f"{prefix}**{label(node)}**")
        lines.extend(render_nodes(node.get("children", []), depth + 1))
    return lines


def load(root, config_path):
    try:
        config = json.loads(config_path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        error(f"cannot read configuration: {exc}")
    if config.get("$schema") != "schema/document-map.schema.json" or config.get("format") != "ugs-document-map/v1" or config.get("schema_version") != 1:
        error("unsupported configuration header")
    for key in ("document", "heading", "heading_zh"):
        if not isinstance(config.get(key), str) or not config[key].strip():
            error(f"missing or invalid {key}")
    document = Path(config["document"])
    if document.is_absolute() or ".." in document.parts:
        error("document must be repository-relative")
    validate_nodes(config.get("nodes"), root)
    target = root / document
    if not target.is_file():
        error(f"mapped document does not exist: {config['document']}")
    return config, target


def generated_section(config):
    heading = f"## {config['heading']}（{config['heading_zh']}）"
    return heading + "\n\n" + "\n".join(render_nodes(config["nodes"])) + "\n\n"


def replace_section(text, config, generated):
    heading = re.escape(config["heading"])
    heading_zh = re.escape(config["heading_zh"])
    match = re.search(r"^##[ \t]+" + heading + r"(?:（" + heading_zh + r"）)?[ \t]*$", text, re.MULTILINE)
    if not match:
        error(f"heading not found: {config['heading']}")
    next_heading = re.search(r"^##[ \t]+", text[match.end():], re.MULTILINE)
    end = match.end() + next_heading.start() if next_heading else len(text)
    return text[:match.start()] + generated + text[end:]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", nargs="?", default=".ugs/document-map.json")
    parser.add_argument("--check", action="store_true", help="fail if README is not generated from the configuration")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    config_path = Path(args.config)
    if not config_path.is_absolute():
        config_path = root / config_path
    try:
        config, target = load(root, config_path)
        original = target.read_text()
        generated = generated_section(config)
        result = replace_section(original, config, generated)
        if args.check:
            if result != original:
                error("README Document Map is not generated from configuration; run scripts/generate_document_map.py")
            print(f"document map generation check passed ({config_path})")
        else:
            target.write_text(result)
            print(f"document map generated: {target}")
    except (OSError, ValueError) as exc:
        print(f"document map generation failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
