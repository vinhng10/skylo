from pathlib import Path
from datetime import datetime
from fastapi import FastAPI, status
from pydantic import BaseModel

if not Path("./event.csv").exists():
    with open("./event.csv", "a") as f:
        f.write(f"VehiclePlate,EntryDateTime,ExitDateTime,Duration\n")


class Event(BaseModel):
    vehicle_plate: str
    entry_date_time: datetime
    exit_date_time: datetime


app = FastAPI()


@app.post("/", status_code=status.HTTP_200_OK)
async def log_event(event: Event) -> Event:
    with open("./event.csv", "a") as f:
        duration = event.exit_date_time - event.entry_date_time
        f.write(
            f"{event.vehicle_plate},{event.entry_date_time},{event.exit_date_time},{duration}\n"
        )
    return event
