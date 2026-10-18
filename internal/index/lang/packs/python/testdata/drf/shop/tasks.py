from proj.celery import app


@app.task(bind=True)
def reindex(self, product_id):
    pass


def helper():
    pass
