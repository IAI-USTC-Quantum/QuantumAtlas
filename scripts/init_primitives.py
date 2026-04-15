#!/usr/bin/env python3
"""
Initialize knowledge graph with primitive definitions.

Usage:
    python scripts/init_primitives.py
"""

import sys
from pathlib import Path

# Add parent directory to path
sys.path.insert(0, str(Path(__file__).parent.parent))

import yaml
from atlas.knowledge import Neo4jClient, Primitive


def load_primitive_from_yaml(yaml_path: Path) -> Primitive:
    """Load primitive definition from YAML file."""
    with open(yaml_path, 'r') as f:
        data = yaml.safe_load(f)
    
    # Map YAML fields to Primitive fields
    return Primitive(
        id=data['id'],
        name=data['name'],
        description=data.get('description', ''),
        category=data['category'],
        complexity=data.get('complexity'),
        references=data.get('references', []),
        tags=data.get('tags', []),
        definition=data.get('definition'),
        prerequisites=data.get('prerequisites', []),
    )


def main():
    """Initialize primitives in Neo4j."""
    print("🔄 Initializing primitives in Neo4j...")
    print("-" * 50)
    
    # Connect to Neo4j
    try:
        client = Neo4jClient()
        client.connect()
        print("✅ Connected to Neo4j")
    except Exception as e:
        print(f"❌ Failed to connect to Neo4j: {e}")
        print("\n💡 Make sure Neo4j is running:")
        print("   docker-compose up -d")
        return 1
    
    # Load primitives from YAML files
    primitives_dir = Path(__file__).parent.parent / "atlas" / "knowledge_graph" / "primitives"
    yaml_files = list(primitives_dir.glob("*.yaml"))
    
    print(f"\n📁 Found {len(yaml_files)} primitive definitions")
    
    created = 0
    for yaml_file in yaml_files:
        print(f"\n📄 Loading {yaml_file.name}...")
        
        try:
            primitive = load_primitive_from_yaml(yaml_file)
            client.create_primitive(primitive)
            print(f"   ✅ Created: {primitive.name} ({primitive.id})")
            created += 1
        except Exception as e:
            print(f"   ❌ Error: {e}")
    
    # Show final stats
    stats = client.get_stats()
    print(f"\n📊 Database stats:")
    for label, count in stats.items():
        print(f"   {label}: {count}")
    
    client.close()
    
    print(f"\n✨ Initialized {created} primitives!")
    return 0


if __name__ == "__main__":
    sys.exit(main())
