"""
Graph Router

Handles Neo4j graph visualization.
"""

from fastapi import APIRouter, Request, HTTPException
from fastapi.responses import HTMLResponse
from typing import Optional, List, Dict, Any

router = APIRouter()


@router.get("/", response_class=HTMLResponse)
async def graph_home(request: Request):
    """Graph explorer home page."""
    config = request.app.state.config
    templates = request.app.state.templates

    # Get graph stats if Neo4j is available
    stats = {"nodes": 0, "relationships": 0, "labels": []}
    try:
        from atlas.knowledge.neo4j_client import Neo4jClient

        client = Neo4jClient(
            uri=config.neo4j_uri,
            username=config.neo4j_user,
            password=config.neo4j_password,
        )
        client.connect()
        stats = client.get_stats()
        client.close()
    except Exception as e:
        stats["error"] = str(e)

    return templates.TemplateResponse(
        request,
        "graph/explorer.html",
        {
            "stats": stats,
            "neo4j_uri": config.neo4j_uri,
            "neo4j_user": config.neo4j_user,
        },
    )


@router.get("/node/{node_type}/{node_id}", response_class=HTMLResponse)
async def graph_node(request: Request, node_type: str, node_id: str):
    """Display node details."""
    config = request.app.state.config
    templates = request.app.state.templates

    node = None
    relationships = []

    try:
        from atlas.knowledge.neo4j_client import Neo4jClient

        client = Neo4jClient(
            uri=config.neo4j_uri,
            username=config.neo4j_user,
            password=config.neo4j_password,
        )
        client.connect()

        # Get node based on type
        if node_type.lower() == "primitive":
            node = client.get_primitive(node_id)
        elif node_type.lower() == "algorithm":
            node = client.get_algorithm(node_id)
            if node:
                relationships = client.get_algorithm_primitives(node_id)
        elif node_type.lower() == "paper":
            node = client.get_paper(node_id)

        client.close()
    except Exception as e:
        pass

    if node is None:
        raise HTTPException(status_code=404, detail=f"Node not found: {node_type}/{node_id}")

    return templates.TemplateResponse(
        request,
        "graph/node.html",
        {
            "node": node,
            "node_type": node_type,
            "relationships": relationships,
        },
    )