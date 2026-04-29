import argparse
import base64
import json
import os
import pathlib
import sys
from typing import Any, Dict, Tuple


PLACEHOLDER_ENV_MAP: Dict[str, str] = {
    "__TOKEN_PLACEHOLDER__": "EASYPROXY_AGGREGATOR_SHARED_TOKEN",
    "__ISSUE91_SUB_URL_PLACEHOLDER__": "EASYPROXY_AGGREGATOR_ISSUE91_SUB_URL_B64",
}


def resolve_env_value(env_name: str) -> str:
    value = os.environ.get(env_name, "").strip()
    if not value:
        return ""

    if env_name.endswith("_B64"):
        try:
            return base64.b64decode(value).decode("utf-8").strip()
        except Exception as exc:
            raise RuntimeError(f"Failed to decode base64 environment value for {env_name}: {exc}") from exc

    return value


def replace_placeholders(value: str, env_map: Dict[str, str]) -> Tuple[str, bool]:
    replaced = value
    unresolved = False
    for placeholder, env_name in env_map.items():
        if placeholder not in replaced:
            continue
        env_value = resolve_env_value(env_name)
        if env_value:
            replaced = replaced.replace(placeholder, env_value)
        else:
            unresolved = True
    return replaced, unresolved


def sanitize_domain_entry(entry: Dict[str, Any], env_map: Dict[str, str]) -> Tuple[Dict[str, Any], bool]:
    unresolved = False
    placeholder_backed = False
    sanitized = dict(entry)

    subs = sanitized.get("sub")
    if isinstance(subs, list):
        new_subs = []
        for item in subs:
            if not isinstance(item, str):
                new_subs.append(item)
                continue
            if any(placeholder in item for placeholder in env_map):
                placeholder_backed = True
            replaced, item_unresolved = replace_placeholders(item, env_map)
            if item_unresolved:
                unresolved = True
                continue
            new_subs.append(replaced)
        sanitized["sub"] = new_subs

    for key, value in list(sanitized.items()):
        if key == "sub" or not isinstance(value, str):
            continue
        if any(placeholder in value for placeholder in env_map):
            placeholder_backed = True
        replaced, item_unresolved = replace_placeholders(value, env_map)
        if item_unresolved:
            unresolved = True
            replaced = ""
        sanitized[key] = replaced

    if unresolved:
        sanitized["enable"] = False
        if isinstance(sanitized.get("name"), str) and "[disabled-missing-secret]" not in sanitized["name"]:
            sanitized["name"] = f'{sanitized["name"]} [disabled-missing-secret]'
    elif placeholder_backed:
        sanitized["enable"] = True
        if isinstance(sanitized.get("name"), str):
            sanitized["name"] = sanitized["name"].replace(" [disabled-missing-secret]", "")

    return sanitized, unresolved


def replace_generic(node: Any, env_map: Dict[str, str]) -> Tuple[Any, bool]:
    if isinstance(node, str):
        replaced, unresolved = replace_placeholders(node, env_map)
        return replaced, unresolved

    if isinstance(node, list):
        values = []
        unresolved = False
        for item in node:
            rendered, item_unresolved = replace_generic(item, env_map)
            values.append(rendered)
            unresolved = unresolved or item_unresolved
        return values, unresolved

    if isinstance(node, dict):
        values: Dict[str, Any] = {}
        unresolved = False
        for key, value in node.items():
            rendered, item_unresolved = replace_generic(value, env_map)
            values[key] = rendered
            unresolved = unresolved or item_unresolved
        return values, unresolved

    return node, False


def materialize(template_path: pathlib.Path, output_path: pathlib.Path) -> None:
    template = json.loads(template_path.read_text(encoding="utf-8"))
    unresolved_entries = []

    domains = template.get("domains")
    template_without_domains = dict(template)
    if "domains" in template_without_domains:
        template_without_domains.pop("domains")
    rendered_template, unresolved_generic = replace_generic(template_without_domains, PLACEHOLDER_ENV_MAP)
    if unresolved_generic:
        raise RuntimeError("Aggregator runtime config still contains unresolved placeholder markers outside domain seed entries.")
    if isinstance(rendered_template, dict):
        template = rendered_template

    if isinstance(domains, list):
        rendered_domains = []
        for entry in domains:
            if not isinstance(entry, dict):
                rendered_domains.append(entry)
                continue
            sanitized, unresolved = sanitize_domain_entry(entry, PLACEHOLDER_ENV_MAP)
            if unresolved:
                unresolved_entries.append(str(sanitized.get("name") or "<unnamed-domain>"))
            rendered_domains.append(sanitized)
        template["domains"] = rendered_domains

    content = json.dumps(template, ensure_ascii=False, indent=4)
    if "PLACEHOLDER" in content:
        raise RuntimeError("Aggregator runtime config still contains unresolved placeholder markers.")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(content, encoding="utf-8")

    if unresolved_entries:
        print(
            "Disabled placeholder-backed aggregator domains because matching secrets were not configured: "
            + ", ".join(unresolved_entries),
            file=sys.stderr,
        )


def main() -> int:
    parser = argparse.ArgumentParser(description="Materialize the native aggregator runtime config.")
    parser.add_argument("--template", required=True, help="Path to the tracked aggregator config template.")
    parser.add_argument("--output", required=True, help="Path to write the runtime config JSON.")
    args = parser.parse_args()

    materialize(pathlib.Path(args.template), pathlib.Path(args.output))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
