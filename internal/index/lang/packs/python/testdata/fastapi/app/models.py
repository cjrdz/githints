from sqlmodel import Field, SQLModel


class UserBase(SQLModel):
    email: str


class User(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    email: str


# Inherits through a project class, which a pattern cannot see.
class UserCreate(UserBase):
    password: str


class UserPublic(SQLModel):
    id: int
