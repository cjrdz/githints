from fastapi import APIRouter, Depends

from app.db import get_session
from app.models import (
    UserCreate,
    UserPublic,
)

router = APIRouter(
    prefix="/users",
    tags=["users"],
)


@router.get("/", response_model=list[UserPublic])
def list_users(session=Depends(get_session)):
    ...


@router.post(
    "/",
    response_model=UserPublic,
    status_code=201,
)
def create_user(user: UserCreate):
    ...


@router.get("/{user_id}")
def read_user(user_id: int):
    ...
