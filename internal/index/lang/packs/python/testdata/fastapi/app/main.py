from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from app.api import users
from app.api.items import router as items_router
from . import health

app = FastAPI(title="demo")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
)
app.include_router(users.router)
app.include_router(items_router)
app.include_router(health.router)


@app.get("/")
async def root():
    return {"message": "ok"}
