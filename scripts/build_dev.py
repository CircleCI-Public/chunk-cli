# build_dev.py
from dotenv import load_dotenv
from e2b import Template, default_build_logger
from template import template

load_dotenv()

if __name__ == "__main__":
    Template.build(
        template,
        "template-tag-dev",
        cpu_count=1,
        memory_mb=1024,
        on_build_logs=default_build_logger(),
    )
