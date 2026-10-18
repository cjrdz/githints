from fastapi import APIRouter
from pydantic import BaseModel, Field

router: APIRouter = APIRouter(prefix="/items")


class ItemIn(BaseModel):
    name: str
    price: float = Field(gt=0)


@router.api_route("/{item_id}", methods=["GET", "HEAD"])
async def read_item(item_id: int):
    ...


@router.websocket("/ws")
async def feed(ws):
    ...
