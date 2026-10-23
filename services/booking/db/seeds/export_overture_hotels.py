"""Export real hotel places from Overture Maps for local load testing."""

import argparse
import csv
import json
import re
import tempfile
from pathlib import Path
from urllib.request import urlopen

import duckdb


CATALOG_URL = "https://stac.overturemaps.org/catalog.json"
BUCKET = "s3://overturemaps-us-west-2/release"
RELEASE_PATTERN = re.compile(r"\d{4}-\d{2}-\d{2}\.\d+")


def latest_release():
    with urlopen(CATALOG_URL, timeout=30) as response:
        release = json.load(response)["latest"].strip("/").split("/")[-1]
    return release


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", default=None, help="Overture release, e.g. 2026-10-21.0")
    parser.add_argument("--limit", type=int, default=50000)
    parser.add_argument("--output", type=Path, default=Path(tempfile.gettempdir()) / "overture_hotels.csv")
    args = parser.parse_args()

    if args.limit <= 0:
        parser.error("--limit must be positive")
    release = args.release or latest_release()
    if not RELEASE_PATTERN.fullmatch(release):
        parser.error("invalid release name")

    output = args.output.expanduser().resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    sql_output = str(output).replace("'", "''")
    parquet_path = f"{BUCKET}/{release}/theme=places/type=place/*"

    conn = duckdb.connect(config={"threads": "4"})
    extension_path = Path(tempfile.gettempdir()) / "booking-duckdb-extensions"
    sql_extension_path = str(extension_path).replace("'", "''")
    conn.execute(f"SET extension_directory='{sql_extension_path}'")
    conn.execute("INSTALL httpfs")
    conn.execute("LOAD httpfs")
    conn.execute("SET s3_region='us-west-2'")
    conn.execute(f"""
        COPY (
            SELECT
                id,
                trim(names.primary) AS name,
                concat_ws(', ',
                    nullif(trim(addresses[1].freeform), ''),
                    nullif(trim(addresses[1].locality), ''),
                    nullif(trim(addresses[1].region), ''),
                    addresses[1].country
                ) AS address,
                'USD' AS currency
            FROM read_parquet('{parquet_path}', filename=true, hive_partitioning=1)
            WHERE basic_category = 'hotel'
              AND names.primary IS NOT NULL
              AND trim(names.primary) <> ''
              AND addresses[1].country IS NOT NULL
              AND (nullif(trim(addresses[1].freeform), '') IS NOT NULL
                   OR nullif(trim(addresses[1].locality), '') IS NOT NULL)
              AND operating_status IS DISTINCT FROM 'permanently_closed'
            LIMIT {args.limit}
        ) TO '{sql_output}' (FORMAT CSV, HEADER TRUE);
    """)

    with output.open(newline="", encoding="utf-8") as exported:
        count = sum(1 for _ in csv.reader(exported)) - 1
    print(f"Exported {count:,} real hotel records from Overture {release} to {output}")


if __name__ == "__main__":
    main()
