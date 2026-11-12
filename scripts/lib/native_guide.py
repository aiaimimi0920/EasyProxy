"""Export the self-contained native installation section from the operator guide."""
from pathlib import Path
import argparse

ROOT = Path(__file__).resolve().parents[2]


def native_guide() -> str:
    text = (ROOT / "docs/deployment.md").read_text(encoding="utf-8")
    section = text.split('<a id="native-install-update"></a>', 1)[1]
    section = section.split('<a id="root-host-deploy-standard"></a>', 1)[0].strip()
    return "# EasyProxy native installation guide\n\n" + section + "\n"


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.write_text(native_guide(), encoding="utf-8")
