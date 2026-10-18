from sqlmodel import Session, create_engine

engine = create_engine("sqlite://")


def get_session():
    with Session(engine) as session:
        yield session
