import argparse
import pathlib
import sys
from typing import Dict


PLACEHOLDER_ENV_MAP: Dict[str, str] = {
    "__KEY_PLACEHOLDER__": "EASYPROXY_AGGREGATOR_SEED_SUB_KEY",
    "__TOKEN_PLACEHOLDER__": "EASYPROXY_AGGREGATOR_SHARED_TOKEN",
}


def materialize(template_path: pathlib.Path, output_path: pathlib.Path) -> None:
    content = template_path.read_text(encoding="utf-8")
    missing = []

    for placeholder, env_name in PLACEHOLDER_ENV_MAP.items():
        if placeholder not in content:
            continue
        value = pathlib.os.environ.get(env_name, "").strip()
        if not value:
            missing.append(env_name)
            continue
        content = content.replace(placeholder, value)

    if missing:
        joined = ", ".join(missing)
        raise RuntimeError(f"Missing required aggregator placeholder secrets: {joined}")

    if "PLACEHOLDER" in content:
        raise RuntimeError("Aggregator runtime config still contains unresolved placeholder markers.")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(content, encoding="utf-8")


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
