from pathlib import Path
from datetime import datetime
from fastapi import FastAPI, status
from pydantic import BaseModel

if not Path("./invoice.csv").exists():
    with open("./invoice.csv", "a") as f:
        f.write(f"VehiclePlate,EntryDateTime,ExitDateTime,Duration\n")


class Invoice(BaseModel):
    vehicle_plate: str
    entry_date_time: datetime
    exit_date_time: datetime


app = FastAPI()


@app.post("/", status_code=status.HTTP_200_OK)
async def send(invoice: Invoice) -> Invoice:
    with open("./invoice.csv", "a") as f:
        duration = invoice.exit_date_time - invoice.entry_date_time
        f.write(
            f"{invoice.vehicle_plate},"
            f"{invoice.entry_date_time},"
            f"{invoice.exit_date_time},"
            f"{duration}\n"
        )
    return invoice
