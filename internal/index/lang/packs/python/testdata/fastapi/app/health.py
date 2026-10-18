from fastapi import APIRouter

router = APIRouter()

# A route-shaped string is not a route.
DOC = "@router.get('/not-a-route')"


@router.get("/health")
def health():
    return {"ok": True}
