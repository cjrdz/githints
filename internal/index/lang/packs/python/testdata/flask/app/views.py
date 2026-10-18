from flask import Blueprint, jsonify

from .models import Post

bp = Blueprint("posts", __name__, url_prefix="/posts")


@bp.route("/")
def index():
    return jsonify([])


@bp.post("/<int:post_id>/like")
def like(post_id):
    ...
