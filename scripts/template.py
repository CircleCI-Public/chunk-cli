# template.py
from e2b import Template, wait_for_timeout

template = (
    Template()
    .from_base_image()
    .set_envs(
        {
            "HELLO": "Hello, World!",
        }
    )
    .set_start_cmd("echo $HELLO", wait_for_timeout(5_000))
)
